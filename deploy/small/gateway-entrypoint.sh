#!/bin/sh
# Applies migrations/postgres/*.sql (idempotent CREATE TABLE IF NOT EXISTS)
# before starting the gateway, then execs it. Render's Free plan doesn't
# support preDeployCommand/one-off jobs, so this runs on every container
# start instead — cheap and safe since every migration is a no-op once
# applied. No-ops entirely when UBAG_POSTGRES_DSN is unset (the small
# profile's default memory-store mode, and docker-compose.small.yml's own
# postgres-migrate service already covers the compose case explicitly).
set -e

if [ -n "${UBAG_POSTGRES_DSN:-}" ]; then
  for f in /app/migrations/postgres/*.sql; do
    psql "$UBAG_POSTGRES_DSN" -v ON_ERROR_STOP=1 -f "$f"
  done
fi

exec /app/ubag-gateway
