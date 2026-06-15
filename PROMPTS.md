# Prompts

This project uses prompts in two places, documented here for transparency.

1. **Runtime prompt** — what the classification stage sends to the model to categorize tokens.
2. **Development prompts** — how the design and implementation were driven with an AI assistant.

---

## 1. Runtime classification prompt

The classification worker claims a batch of extracted tokens and asks the model to categorize
all of them in a single call (one request per batch — cheaper and fewer round-trips than one
call per token). This is the prompt that backs `internal/llm/claude.go`.

### System prompt

```
You are an entity classifier in a document-processing pipeline. For each token you are given,
assign exactly one category:

- COMPANY  — a business, organization, institution, or brand (e.g. "Acme Corporation", "the Federal Reserve").
- PERSON   — a named individual (e.g. "Sarah Johnson").
- ADDRESS  — a physical location or address: street address, city, region, or country
             (e.g. "500 Market Street", "San Francisco").
- DATE     — a calendar date or clearly date-like expression (e.g. "January 15, 2024", "2024-03-01").
- UNKNOWN  — none of the above, or genuinely ambiguous from the text given.

Each token includes an `nlp_type` produced by an upstream extractor. Treat it as a hint, not
ground truth — the extractor is approximate and sometimes mislabels. Decide from the token text
itself.

Rules:
- Choose the single best category. Do not invent new categories.
- Use UNKNOWN when the text is ambiguous rather than guessing; reflect that in a lower confidence.
- `confidence` is your own calibrated certainty from 0.0 to 1.0.
- Keep `reasoning` to one short clause.

Return ONLY a JSON array, one object per input token, in the same order, with no surrounding
text or markdown:

[
  { "category": "COMPANY", "confidence": 0.97, "reasoning": "corporate suffix 'Corporation'" }
]
```

### User message (per batch)

```
Classify these tokens:

1. text="Acme Corporation"   nlp_type=ORG
2. text="Sarah Johnson"       nlp_type=PERSON
3. text="500 Market Street"   nlp_type=ADDRESS
4. text="January 15, 2024"    nlp_type=DATE
5. text="Q3"                  nlp_type=PERSON
```

### Expected shape of the response

```json
[
  { "category": "COMPANY", "confidence": 0.98, "reasoning": "corporate suffix 'Corporation'" },
  { "category": "PERSON",  "confidence": 0.97, "reasoning": "given name + surname" },
  { "category": "ADDRESS", "confidence": 0.95, "reasoning": "street number + street name" },
  { "category": "DATE",    "confidence": 0.99, "reasoning": "explicit calendar date" },
  { "category": "UNKNOWN", "confidence": 0.55, "reasoning": "ambiguous fiscal-quarter token; nlp_type likely wrong" }
]
```

### Engineering notes

- **Determinism:** call with `temperature: 0` so the same token classifies consistently.
- **Batching:** all tokens in the claimed batch go in one request; keep batches modest
  (`CLASSIFY_BATCH_SIZE`) so latency stays bounded while the claim transaction is open.
- **Robust parsing:** if the array length doesn't match the input or JSON fails to parse, fall
  back to `UNKNOWN` for the affected tokens rather than failing the whole batch.
- **Resilience:** wrap the call in retry with exponential backoff for `429`/`5xx`.
- **Model:** `claude-haiku-4-5-20251001` by default — a short, well-specified classification task
  doesn't need a larger model, which keeps cost and latency down. Override with `ANTHROPIC_MODEL`.

---

## 2. Development prompts

The design was not produced by a single prompt — it was worked out through dialogue. Each major
decision (Postgres-as-queue vs. a message broker, atomic extraction vs. incremental
classification, the reject-if-in-flight rerun policy, and where crash recovery actually comes
from) was argued with its trade-offs both ways before a choice was made and captured in
`ARCHITECTURE.md`. AI was used as a collaborator with human judgment as the integrator —
proposals were interrogated and corrected, not accepted wholesale. Implementation then followed
the locked design.

### Toolchain

- **Claude (chat)** — design dialogue, the initial scaffold, and the runtime classification
  prompt in §1.
- **Claude Code** — implementation against the agreed design (store wiring, HTTP handlers, the
  classifier), with `go build` / `go vet` / `go test` run in the loop.
- **Codex** — an independent review pass. It surfaced two medium issues that were then fixed: a
  classification worker that could panic on a short result slice before the store's length check,
  and unvalidated `limit` / `offset` and batch-size inputs that could turn a client error into a
  503 or stall the worker. Using a second, independent model to review the first's output was a
  deliberate verify-don't-trust step.

### Representative implementation prompt

Implementation was delegated to Claude Code as contract-first prompts: an explicit task, the
constraints that must hold, and a verification step — with the reasoning embedded so the agent
made the right call rather than guessing. Example (adding the health probes):

> Add Kubernetes-style liveness and readiness endpoints to the API.
> - `GET /healthz` → liveness: 200 if the process is serving. Must **not** check the DB — if it
>   did, a transient Postgres blip would fail every pod's liveness and make Kubernetes restart
>   the whole fleet, turning a short outage into a cascading one.
> - `GET /readyz` → readiness: pings Postgres (2s timeout); 503 if unreachable.
> - Constraints: routes + docs only; no K8s manifests; stdlib `net/http`; match existing style;
>   run `go build ./...` and `go vet ./...` when done.

The store-wiring and Claude-classifier prompts followed the same shape — contract, constraints,
and a build/test step the agent had to satisfy before finishing.

### Decisions argued in dialogue

The questions that drove the design, each resolved with trade-offs named in `ARCHITECTURE.md`:

- Postgres as the work queue vs. a message broker — single source of truth vs. transport throughput.
- Atomic extraction vs. incremental, batched classification.
- Reject-if-in-flight vs. always-accept-and-abort on document re-submission.
- Where crash recovery comes from — the transaction boundary, not a separate reconciler.

