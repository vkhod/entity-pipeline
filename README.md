# entity-pipeline

A two-stage document-processing pipeline: **extraction** (NLP → entity tokens) →
**classification** (LLM → `COMPANY` / `PERSON` / `ADDRESS` / `DATE` / `UNKNOWN`).
The stages are independent and scale separately. Postgres is the single source of
truth *and* the work queue.

See [`ARCHITECTURE.md`](./ARCHITECTURE.md) for the design rationale and trade-offs,
and [`docs/architecture.mermaid`](./docs/architecture.mermaid) for the diagram.

## Prerequisites

- Docker + Docker Compose (everything runs in containers)
- `jq` and `curl` — only for the demo script
- Go 1.26+ — only if you want to build or run tests outside Docker

## Quick start

```bash
./start.sh           # or: make run
```

This brings up Postgres (schema auto-applied on first boot), the API on
`:8080`, one extraction worker, and one classification worker.

## API

```bash
# Submit a document for processing
curl -X POST http://localhost:8080/process \
  -H 'Content-Type: application/json' \
  -d '{"document_id":"doc-1","text":"Acme Corporation hired Sarah Johnson on January 15, 2024."}'

# Check status (progress + per-stage durations)
curl http://localhost:8080/documents/doc-1/status

# List tokens, optionally filtered
curl "http://localhost:8080/documents/doc-1/tokens?classification=PERSON"
```

| Endpoint | Method | Returns |
|---|---|---|
| `/process` | POST | `202` queued/rerun · `409` already in flight · `400` bad input |
| `/documents/{id}/status` | GET | status, progress, `durations_ms` |
| `/documents/{id}/tokens` | GET | tokens; filters: `classification`, `page`, `status`; paging: `limit`, `offset` |
| `/healthz` | GET | **liveness** — `200 ok` (process alive, no dependency checks) |
| `/readyz` | GET | **readiness** — `200 ready` / `503 not ready` (pings Postgres) |

## Scaling a stage

Each worker is stateless; `SELECT … FOR UPDATE SKIP LOCKED` hands each replica a
disjoint slice of work with no inter-worker coordination:

```bash
docker compose up --build --scale classification-worker=5
```

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://pipeline:pipeline@localhost:5432/pipeline?sslmode=disable` | Postgres DSN |
| `HTTP_ADDR` | `:8080` | API listen address |
| `CLASSIFIER` | `mock` | `mock` or `claude` |
| `CLASSIFY_BATCH_SIZE` | `10` | tokens claimed per classification transaction |
| `CLASSIFY_DEMO_DELAY` | `50ms` | per-token mock delay so progress is visible (demo only) |
| `POLL_INTERVAL` | `500ms` | idle poll interval when no work is available |
| `RETRY_BACKOFF` | `2s` | backoff after a transient error |
| `ANTHROPIC_API_KEY` | — | required when `CLASSIFIER=claude` |
| `ANTHROPIC_MODEL` | `claude-haiku-4-5-20251001` | model for the real classifier |

## Using the real Claude classifier

The `.env` file at the project root is the single place for all runtime configuration.
It is loaded automatically by Docker Compose and is already gitignored so the key
stays local.

### Step 1 — set the key in `.env`

Open `.env` and fill in your Anthropic API key:

```
CLASSIFIER=claude
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-haiku-4-5-20251001
```

The key is only needed at **runtime** (not at build time), so this step can happen
any time before you start the containers.

### Step 2 — start the stack

```bash
./start.sh        # equivalent: docker compose up --build
```

Docker Compose reads `.env` automatically and passes `CLASSIFIER`, `ANTHROPIC_API_KEY`,
and `ANTHROPIC_MODEL` to the `classification-worker` container. The extraction worker
and API server are unaffected.

### Step 3 — run the Claude integration test (optional)

`go test` does not load `.env` automatically — variables must already be in the shell
environment. Source them first, then run the test:

**macOS / Linux (bash/zsh):**
```bash
set -a
source .env
set +a
go test ./internal/llm/ -run TestClaudeClassifier -v
```

**Windows PowerShell:**
```powershell
Get-Content .env | Where-Object { $_ -match '^[A-Z_]+=.' -and $_ -notmatch '^#' } |
    ForEach-Object { $_ -replace '\s+#.*$', '' } |
    ForEach-Object { $n, $v = $_ -split '=', 2; Set-Item "env:$n" $v }
go test ./internal/llm/ -run TestClaudeClassifier -v
```

The test skips automatically if `ANTHROPIC_API_KEY` is not set, so `go test ./...`
always stays green with no key.

> **No key?** Everything works without one. Leave `CLASSIFIER=mock` in `.env` (or
> omit the variable entirely) and the pipeline runs end-to-end using the built-in
> deterministic classifier with no external calls.

## Test documents

- `testdata/small.txt` — ~8 entities
- `testdata/medium.txt` — ~35 entities
- `testdata/large.txt` — 120+ entities

## Demo

```bash
./scripts/demo.sh        # or: make demo
```

Walks the six scenarios: happy path, progress visibility, filtered token query,
full rerun, concurrent documents, and partial-rerun crash recovery (the last is
guided manual steps using `docker compose stop/start classification-worker`).

## Tests

```bash
go test ./...
```

Integration tests spin up a real Postgres via `testcontainers-go` (see `test/`).

## Project layout

```
cmd/api            API entrypoint
cmd/worker         worker entrypoint (--stage=extraction|classification)
internal/model     domain types
internal/config    env configuration
internal/nlp       Extractor interface + rule-based mock
internal/llm       Classifier interface + mock + Claude (real) + factory
internal/queue     WorkQueue port (the broker-swap seam)
internal/store     Postgres implementation (queue + API methods)
internal/worker    stage loop
internal/api       HTTP handlers
migrations         schema.sql (auto-applied by Postgres on first boot)
testdata           small / medium / large documents
docs               architecture diagram
```

## Status

The default `CLASSIFIER=mock` runs the whole pipeline with no external dependencies.
`CLASSIFIER=claude` switches to the real Anthropic Messages API classifier — one
request per batch, with retry/backoff and length-safe result handling
(`internal/llm/claude.go`). The interface, worker, and store are unchanged between
the two modes.
