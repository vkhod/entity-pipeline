#!/usr/bin/env bash
set -euo pipefail
# Bring up Postgres, the API, and both worker stages.
exec docker compose up --build "$@"
