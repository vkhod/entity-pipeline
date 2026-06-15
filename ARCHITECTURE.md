# Architecture & Design

Document-processing pipeline that turns raw text into classified entity tokens through two
independent stages:

1. **Extraction** — an NLP step that scans text and emits *tokens* (an entity occurrence with
   text, type, and position).
2. **Classification** — an LLM step that assigns each token a category:
   `COMPANY`, `PERSON`, `ADDRESS`, `DATE`, or `UNKNOWN`.

The stages scale independently, support reruns (both crash-recovery and deliberate
reprocessing), and track how long each stage takes. The whole thing runs locally via Docker
Compose.

![pipeline](./docs/architecture.mermaid)

---

## 1. Technology selection

| Concern | Choice | Why |
|---|---|---|
| Language | **Go** | Goroutine worker pools, tiny static binaries, trivial horizontal scaling of stateless workers, strong stdlib HTTP. |
| Datastore + queue | **PostgreSQL** | One store that is both the source of truth and the work queue (`FOR UPDATE SKIP LOCKED`). Transactional claim-and-update gives idempotency and rerun semantics nearly for free. |
| Local dev | **Docker Compose** | One command stands up Postgres, the API, and both worker stages; workers scale with `--scale`. |
| HTTP | **stdlib `net/http`** (Go 1.22 routing) | Method + path-pattern routing landed in 1.22; no framework needed for four endpoints. |
| DB driver | **pgx v5 / pgxpool** | Fast, well-maintained, first-class `SKIP LOCKED` and transaction support. |
| Container image | **distroless static** | Minimal attack surface and image size for the two binaries. |

### Why one Postgres instead of a message broker

The assignment frames the two stages as a producer/consumer pipeline, which usually suggests a
broker (Kafka, NATS JetStream, Pub/Sub). For this system, a single Postgres is the better fit:

- **One source of truth.** Document state, token data, and queue state are the *same* rows. A
  worker claims work and writes results in one transaction, so there is no window where the
  queue and the database disagree.
- **Reruns and idempotency come from the data model, not extra machinery.** "Where did we get
  to" is answered by reading row state, not by replaying a log.
- **Fewer moving parts to operate** for a POC, while keeping the door open to a broker later
  (see the `WorkQueue` seam in §5 and the scale-out path in §8).

A broker decouples transport from storage, which matters at high throughput — but it also
reintroduces two-system consistency (the queue offset and the database can diverge), which is
exactly the problem we get to avoid here.

### Where Python would win

The job spec lists Go and Python. Python was considered specifically for the
extraction/retrieval layer — spaCy/transformers for real NER, and the richer client ecosystem
for embeddings and RAG. The deliberate choice is Go for the pipeline core (concurrency,
operational simplicity, single-binary deploys) with the NLP/LLM steps hidden behind interfaces
(`Extractor`, `Classifier`) so a Python service could back either one without touching the rest
of the system.

---

## 2. Architecture

Three process types, all stateless except Postgres:

- **API** (`cmd/api`) — accepts documents, reports status, serves tokens. Holds no state; any
  number of replicas can sit behind a load balancer.
- **Extraction worker** (`cmd/worker --stage=extraction`) — claims pending documents, runs the
  NLP step, writes tokens.
- **Classification worker** (`cmd/worker --stage=classification`) — claims batches of extracted
  tokens, runs the LLM step, writes classifications.

Both workers are the same binary selected by a flag, run as two Compose services.

### How work is claimed

Each worker loops: **claim → process → commit**.

```
SELECT … FROM <work> WHERE <not-done> ORDER BY … FOR UPDATE SKIP LOCKED LIMIT n
```

`SKIP LOCKED` is the heart of the design. Each worker locks the rows it claims and *skips* rows
already locked by others, so N replicas partition the outstanding work among themselves with
**zero inter-worker coordination** — no leader, no assignment table, no broker. Scaling a stage
is literally running more copies of that stage's process:

```bash
docker compose up --scale classification-worker=5
```

The row locks are held until the transaction commits, which is what makes recovery automatic
(§6).

---

## 3. Data model

Two tables. The work queue is not a separate table — the domain rows *are* the queue.

### `documents` (the manifest)

| Column | Notes |
|---|---|
| `id` (PK) | The client-supplied `document_id`. Natural key, no surrogate. |
| `status` | `pending → classifying → completed` (or `failed`). |
| `generation` | Bumped on each full rerun — lineage / fencing token. |
| `source_text` | Inline for the POC (object storage in production). |
| `total_tokens`, `classified_count` | Denormalized counters that drive progress and completion. |
| `extraction_started_at` / `…_completed_at` | Stage 1 timing. |
| `classification_started_at` / `…_completed_at` | Stage 2 timing. |
| `error`, `created_at`, `updated_at` | — |

### `tokens`

| Column | Notes |
|---|---|
| `id` (PK) | Surrogate `BIGINT IDENTITY`. |
| `document_id` (FK) | `ON DELETE CASCADE`. |
| `ordinal` | Extraction sequence; `UNIQUE(document_id, ordinal)`. |
| `text`, `nlp_type`, `page`, `sentence`, `char_offset` | Extraction output + position. |
| `classification`, `confidence`, `reasoning` | Classification output (null until classified). |
| `status` | `extracted → classified`. |
| `created_at`, `classified_at` | — |

### Indexes

- Read path: `(document_id)`, `(document_id, classification)`, `(document_id, page)`.
- Queue path (partial indexes on the hot predicate so claims are cheap):
  `tokens(document_id, ordinal) WHERE status='extracted'` and
  `documents(created_at) WHERE status='pending'`.

### The key asymmetry: atomic extraction, incremental classification

This is the most important design decision, and the two stages are deliberately *not*
symmetric:

- **Extraction is atomic per document.** All token inserts, the `total_tokens` count, the
  timestamps, and the `pending → classifying` flip happen in **one transaction**. A partial
  token set is therefore *unrepresentable*: either every token for a document exists, or none
  do. A crash mid-extraction rolls back to zero and the document is simply re-extracted.

- **Classification is incremental and batched.** A worker claims a batch of `extracted` tokens
  (`SKIP LOCKED`), classifies them, and in the *same* transaction writes each result and bumps
  the document's `classified_count`. Progress advances batch by batch and is visible in real
  time. Completion is a race-safe conditional update — the worker whose commit pushes
  `classified_count` to `total_tokens` also flips the document to `completed`:

  ```sql
  UPDATE documents
     SET classified_count = classified_count + $n,
         status = CASE WHEN classified_count + $n >= total_tokens THEN 'completed' ELSE status END,
         …
   WHERE id = $1
  ```

Extraction is cheap and produces a structurally-constrained result, so atomicity is the simplest
correct choice. Classification is the expensive, long-running, parallelizable part, so it is
chunked for progress visibility, bounded lock-hold time, and small crash blast-radius.

---

## 4. Batch size

`CLASSIFY_BATCH_SIZE` (default 10) is a tuning knob, not a formula. It trades off:

- **Smaller** → smoother progress, shorter lock/connection hold, smaller blast-radius on crash,
  finer work distribution across replicas.
- **Larger** → fewer per-claim round-trips and (for a real LLM) more tokens per API call.

It should stay bounded below `total_tokens / parallelism` so one worker can't grab everything
and starve the others. Tune empirically; the default is a reasonable demo value.

---

## 5. Communication contracts

There are three contract layers, not one. The middle one is the interesting part: because the
stages communicate through shared database state rather than a message envelope, the contract is
a **schema plus a state-transition protocol**.

### (a) External HTTP API

`POST /process`
```json
{ "document_id": "doc-1", "text": "Acme Corporation hired Sarah Johnson on January 15, 2024." }
```
- `202 Accepted` — queued (new) or accepted for full rerun (terminal document)
- `409 Conflict` — the document is currently `pending` or `classifying`
- `400 Bad Request` — missing `document_id` or `text`

`GET /documents/{id}/status`
```json
{
  "document_id": "doc-1",
  "status": "classifying",
  "generation": 1,
  "progress": { "classified": 12, "total": 30 },
  "durations_ms": { "extraction": 84, "classification": null }
}
```
`durations_ms` are computed from the stage timestamps and stay `null` until a stage completes.

`GET /documents/{id}/tokens?classification=PERSON&page=1&limit=100&offset=0`
returns the matching tokens, ordered by `ordinal`, with paging.

### (b) Internal stage handoff (the distinctive one)

| | Producer writes | Consumer claims | On success |
|---|---|---|---|
| Extraction → Classification | tokens with `status='extracted'`; document → `classifying` | rows `WHERE status='extracted'` via `SKIP LOCKED` | token → `classified`; counter bumped |

The "message" is a row transition. The transaction boundary *is* the delivery guarantee:
**commit is the acknowledgement.** There is no separate ack to lose and no broker offset to fall
out of sync with the data.

### (c) Pluggable providers

```go
type Extractor interface {
    Extract(ctx context.Context, text string) ([]Entity, error)
}

type Classifier interface {
    Classify(ctx context.Context, tokens []Entity) ([]Result, error)
}
```

The mock implementations run the full pipeline with no external dependencies. The real
classifier (`CLASSIFIER=claude`, Anthropic Messages API) is a drop-in behind the same
interface — see `internal/llm/claude.go`.

### The scaling seam: `WorkQueue`

Both claim operations sit behind a `WorkQueue` port (`internal/queue`). Today it is implemented
over Postgres `SKIP LOCKED`. Swapping in a broker means writing a new adapter — **but it is not
a zero-change swap**: the current model claims and commits inside one transaction, whereas a
broker requires *claim-then-release* with explicit acks and separate idempotency handling.
Transport and processing model are coupled; the seam makes the change localized and explicit
rather than free.

---

## 6. Reruns & recovery

The mental model: **idempotency here is *state* idempotency, not *content* idempotency.** Any
prior state converges to a clean, well-defined end state. It does **not** mean identical output
— real NLP and LLM calls are non-deterministic, and producing fresh results is the whole point
of reprocessing.

### Partial rerun (crash recovery — no explicit trigger)

Nothing special happens. On restart, workers find whatever isn't terminal and resume:
- Tokens left `extracted` (their classifying transaction never committed) are re-claimed.
- Extraction never repeats for a document already past it — the manifest shows it done.

Recovery is "re-claim whatever isn't terminal," which falls out of the claim mechanism for free.

### Full rerun (deliberate reprocessing)

Re-`POST /process` with an existing **terminal** `document_id`. In one transaction the system
deletes the old tokens, resets the manifest counters and timestamps, **bumps `generation`**, and
re-extracts. Deleting first guarantees no stale tokens survive even if the new extraction yields
fewer of them.

### Concurrency policy: reject-if-in-flight

If a `POST /process` arrives for a document that is still `pending` or `classifying`, it is
rejected with `409`. Full rerun is only allowed from a terminal state. This avoids the
rerun-versus-live-worker race entirely and matches the assignment's framing. The document `id`
as primary key (with `INSERT … ON CONFLICT`) prevents duplicate manifests, and `generation`
acts as a fencing token.

*Production alternative (documented, not implemented):* always-accept with generation-versioned
tokens, so a rerun aborts in-flight work and restarts cleanly. More flexible, more moving parts.

---

## 7. Duration tracking

Four timestamps on `documents` capture the current run: extraction start/finish and
classification start/finish. The API computes `durations_ms` from them. For per-rerun history
you would normalize this into a `processing_runs` table keyed by `(document_id, generation)`;
the four-timestamp approach is the simpler choice that satisfies the requirement for the latest
run.

---

## 8. Trade-off analysis

### What Postgres-as-a-queue buys us

One store = one source of truth; transactional claim+commit gives idempotency, atomic
extraction, and race-safe completion almost for free; far fewer moving parts to run.

### Where it ceases to be the right tool

Honest about the ceilings, roughly in the order they bite:

1. **Connections** — every worker holds a pooled connection; thousands of workers exhaust
   Postgres first. *Fix:* PgBouncer.
2. **Single-primary write ceiling** — all writes hit one primary; the practical limit is on the
   order of thousands to tens of thousands of small transactions per second.
3. **Hot-row contention** — the per-document counter row is updated on every batch; very high
   fan-out concurrency contends on it.
4. **Vacuum / bloat** — high-churn `UPDATE`/`DELETE` (status flips, rerun deletes) demands
   autovacuum tuning.

### The scale-out path

When those ceilings bind, split the roles the single Postgres is currently playing:

- a **broker** for transport (Pub/Sub, Kafka, NATS JetStream),
- a **transactional store** for manifests (Postgres / Spanner / CockroachDB),
- a **wide-column store** for token volume (Bigtable / Cassandra),
- **object storage** for raw document bytes.

The cost of splitting is the two-system consistency we deliberately avoided: claim-then-release
with explicit acks, plus idempotency keys so redelivered work doesn't double-write. The
`WorkQueue` seam (§5) is where that adapter goes.

### Other deliberate POC simplifications

- `source_text` inline rather than object storage.
- `schema.sql` via the Postgres init hook rather than a migration tool (`golang-migrate` in
  production).
- No multi-tenancy: with tenants, `documents.id` becomes a composite `(tenant_id, document_id)`
  and indexes/queries gain a leading `tenant_id`.

---

## 9. Failure scenarios

One transactional source of truth makes every failure reduce to the same statement: **a
transaction either committed or it didn't**, and recovery is **re-claim whatever isn't
terminal**.

**1. Classifier crashes after calling the model but before persisting.**
The claim transaction never commits, so the tokens revert from locked-`extracted` back to
plain `extracted` and another worker re-claims them. The only cost is the wasted recompute on
that batch. Because commit is the acknowledgement, there is no broker-style gap where work is
marked done but results are missing.

**2. Extraction crashes midway.**
Extraction is atomic, so a partial token set is unrepresentable. The crash rolls back to zero
tokens and the document is re-extracted from scratch — cheap, because extraction is the cheap
stage. *Scale caveat:* for documents with millions of tokens, "all in one transaction" becomes
heavy; the upgrade is to extract in idempotent batches keyed by `ordinal` so re-extraction
dedups instead of redoing everything.

**3. Database becomes unavailable.**
The pipeline pauses safely — nothing is lost or corrupted because all progress lives in
committed transactions. Workers retry with exponential backoff and jitter; the API returns
`503` and fails its readiness check so a load balancer stops routing to it. When Postgres
returns, work resumes exactly where it stopped. The honest limit: availability is bounded by
Postgres availability, so production needs HA (Patroni, Cloud SQL HA, or a multi-AZ managed
primary with automated failover).

---

## 10. Implemented vs. mocked

- **Runs end to end today** with `CLASSIFIER=mock` and the rule-based extractor — no external
  dependencies, fully demonstrating extraction, batched classification, progress, reruns, and
  crash recovery.
- **Mocked:** the NLP engine (rule-based regex extractor) and the LLM (deterministic
  type→category mapping with a small per-token delay so progress is observable).
- **Real, drop-in:** `CLASSIFIER=claude` targets the Anthropic Messages API behind the same
  `Classifier` interface; enabling it changes no pipeline code.
- **Production gaps, by design:** object storage for source bytes, a migration tool, HA Postgres
  or the broker-based scale-out, multi-tenancy, and richer per-rerun duration history.

---

## 11. Health checks & GKE operations

*This section describes how the system would operate on Kubernetes (e.g. GKE). The POC itself
runs on Docker Compose; no manifests are included.*

### Liveness vs. readiness — the core rule

Kubernetes runs two independent probe types:

- **Liveness** (`GET /healthz`) — answers "is the process alive?" A failed liveness probe
  causes the pod to be restarted. It must return `200` as long as the Go process is up and the
  HTTP server is accepting connections. **It must not check the database.** If liveness checked
  Postgres and the DB had a transient blip, every API pod would fail simultaneously, Kubernetes
  would restart them all, and a short outage would cascade into a fleet-wide restart storm.

- **Readiness** (`GET /readyz`) — answers "should this pod receive traffic?" A failed readiness
  probe removes the pod from the load balancer's endpoint list without restarting it. Once the
  DB recovers, the pod passes its probe and is added back automatically. This is the right place
  for a Postgres ping. The handler wraps the ping in a 2-second timeout so a hung DB does not
  hang the probe goroutine indefinitely.

### API pods

Both probes are implemented in `internal/api/server.go`. Suggested GKE Deployment snippet:

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  initialDelaySeconds: 5
  periodSeconds: 10
```

### Workers — no HTTP probes; autoscale on queue depth

Workers are queue consumers, not HTTP servers. They have no listening port, so HTTP probes are
not applicable. Two observations:

1. **Liveness** can be approximated with a `livenessProbe.exec` that checks whether the
   goroutine is making progress (e.g. a heartbeat file updated each claim loop), but for a
   simple claim-loop worker a process-restart policy (`restartPolicy: Always`) is often enough.

2. **Autoscaling signal is queue depth, not CPU.** The right metric for horizontal scaling is
   the count of tokens waiting to be classified:

   ```sql
   SELECT COUNT(*) FROM tokens WHERE status = 'extracted';
   ```

   [KEDA](https://keda.sh/) (Kubernetes Event-Driven Autoscaler) has a native Postgres scaler
   that can drive a `HorizontalPodAutoscaler` directly from this query — scale-to-zero when
   idle, scale-out when a large document lands.

### Scale-down safety

Scale-down is safe by design and requires no special handling. When Kubernetes sends `SIGTERM`
to a worker pod, the signal context (`signal.NotifyContext`) is cancelled, the claim loop exits
cleanly, and any in-flight transaction is rolled back. The rolled-back tokens revert to
`extracted` and are immediately re-claimable by the remaining replicas via `SKIP LOCKED` — no
coordination, no lost work.
