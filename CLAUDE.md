# CLAUDE.md — entity-pipeline

## Project
Two-stage document-processing pipeline: **extraction (NLP) → classification (LLM)**.
Postgres is both source of truth and work queue (`SELECT … FOR UPDATE SKIP LOCKED`).
Full design rationale in `ARCHITECTURE.md`. This is a **fully implemented take-home assignment**
(Go 1.26, pgx/v5, testcontainers-go). All phases are complete; the pipeline runs end to end.

## Build & run
```bash
go build ./...       # verify compile (go.sum is committed; no tidy needed)
go test ./...        # 17 tests: unit + integration (testcontainers spins real Postgres)
./start.sh           # docker compose up --build (Postgres + API + 2 workers)
```

## Key files
- `internal/store/store.go` — Postgres implementation of WorkQueue + API reads/writes (all methods implemented)
- `internal/worker/worker.go` — claim→process→commit loop with progress logging; stateless/scalable
- `internal/api/server.go` — HTTP handlers; stdlib net/http with Go 1.22 method+path routing
- `migrations/schema.sql` — applied automatically by Postgres on first boot via docker-entrypoint-initdb.d
- `scripts/demo.sh` — six demo scenarios (happy path through crash recovery)
- `test/integration_test.go` — 6 integration tests; TestMain uses ping-retry loop (30×500ms) for container readiness
- `REVIEW_NOTES.md` — gitignored interview Q&A for all phases (do not commit)

## API endpoints
| Endpoint | Notes |
|---|---|
| `POST /process` | 202 created/reran · 409 in-flight · 400 bad input |
| `GET /documents/{id}/status` | status, progress, durations_ms |
| `GET /documents/{id}/tokens` | filters: classification, status, page; paging: limit, offset |
| `GET /healthz` | **liveness** — 200 always (no DB check; process-alive only) |
| `GET /readyz` | **readiness** — 200/503 (pings Postgres with 2s timeout) |

## Critical design decisions (know these for review)
- **Concurrent first-submit race**: `sqlInsertPending` uses `ON CONFLICT (id) DO NOTHING` + `RowsAffected()` check; without this, two simultaneous POSTs for a new doc_id both see "no row" and one crashes with PK violation.
- **Zero-entity edge case**: `sqlFinishExtractionEmpty` flips directly to `completed` (skips `classifying`); without it, a doc with no entities stays in `classifying` forever.
- **Atomic completion flip**: `sqlBumpCounterMaybeComplete` uses `CASE WHEN classified_count + $n >= total_tokens THEN 'completed'` — safe when a batch spans multiple docs or multiple workers race.
- **Cross-doc batches**: `sqlClaimExtractedTokens` orders globally by `(document_id, ordinal)`, so one batch can span N documents; `countPerDoc` map fires the completion check once per doc.
- **Crash recovery is free**: worker crash → transaction rollback → tokens revert to `extracted` → SKIP LOCKED re-claims them on restart.
- **Liveness must NOT check DB**: a transient Postgres blip would make every pod fail liveness → Kubernetes restarts the entire fleet (cascading outage). `/healthz` is process-alive only; `/readyz` does the DB ping.

## Conventions
- All SQL in `const` blocks at top of `store.go`; no query building in business logic (except positional `$N` numbering in `ListTokens` dynamic filter).
- `defer tx.Rollback(ctx)` + explicit `tx.Commit(ctx)` throughout (idiomatic pgx; Rollback after Commit is a no-op).
- Nullable DB columns scan into Go pointer types (`*string`, `*float64`, `*time.Time`).
- Worker errors log and retry (backoff); no panic. `failed` status is reserved but not written (known POC gap).
- `pgx.CollectRows` used for result sets; `tx.Query` (not pool.Query) for locked queries so locks stay in the transaction.

## Environment variables
See `README.md` §Configuration. Key: `CLASSIFIER` (mock|claude), `CLASSIFY_BATCH_SIZE` (10),
`CLASSIFY_DEMO_DELAY` (50ms), `ANTHROPIC_API_KEY` (only for `CLASSIFIER=claude`).

## What is mocked vs real
- NLP extractor: regex rule-based mock (`internal/nlp/mock.go`) — same `Extractor` interface; real NER is a drop-in.
- Classifier: `CLASSIFIER=mock` (deterministic, no network) or `CLASSIFIER=claude` (real Anthropic Messages API client in `internal/llm/claude.go`).
- Integration tests: real Postgres via testcontainers-go (postgres:16 module); no mocks at the DB layer.
