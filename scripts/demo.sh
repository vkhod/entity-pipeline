#!/usr/bin/env bash
# Demo walkthrough of the six required scenarios. Run after `./start.sh` is up.
# Requires: curl, jq.  Run from the repo root.
set -euo pipefail
BASE=${BASE:-http://localhost:8080}

post() { # post <id> <file>
  curl -s -X POST "$BASE/process" -H 'Content-Type: application/json' \
    -d "{\"document_id\":\"$1\",\"text\":\"$(tr '\n' ' ' < "testdata/$2" | sed 's/"/\\"/g')\"}"
}

echo "== 1. Happy path: process a small document end to end =="
post doc-small small.txt | jq .

echo "== 2. Progress visibility: poll status while it classifies =="
for _ in 1 2 3 4 5; do curl -s "$BASE/documents/doc-small/status" | jq '{status, progress, durations_ms}'; sleep 1; done

echo "== 3. Query tokens filtered by classification =="
curl -s "$BASE/documents/doc-small/tokens?classification=PERSON" | jq .

echo "== 4. Full rerun: re-POST the same id once it has completed (data is replaced) =="
post doc-small small.txt | jq .

echo "== 5. Concurrent documents: process three at once =="
for d in small medium large; do post "doc-$d" "$d.txt" >/dev/null & done; wait
for d in small medium large; do curl -s "$BASE/documents/doc-$d/status" | jq '{document_id, status, progress}'; done

echo "== 6. Partial rerun (crash recovery) — manual steps =="
cat <<'NOTE'
  1. POST the large document and let classification begin:
        curl -s -X POST http://localhost:8080/process -H 'Content-Type: application/json' \
          -d "{\"document_id\":\"doc-crash\",\"text\":\"$(tr '\n' ' ' < testdata/large.txt | sed 's/"/\\"/g')\"}" | jq .
  2. While status is 'classifying', stop a classification worker:
        docker compose stop classification-worker
  3. The status endpoint shows progress frozen mid-way (e.g. 40/120).
  4. Restart it:
        docker compose start classification-worker
  5. It resumes from where it left off and reaches 'completed'.
     No token is re-classified twice (uncommitted work simply rolled back).
NOTE
