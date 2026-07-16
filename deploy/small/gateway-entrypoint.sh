#!/bin/sh
# Applies migrations/postgres/*.sql (idempotent CREATE TABLE IF NOT EXISTS)
# before starting the gateway, then execs it. Render's Free plan doesn't
# support preDeployCommand/one-off jobs, so this runs on every container
# start instead — cheap and safe since every migration is a no-op once
# applied. No-ops entirely when UBAG_POSTGRES_DSN is unset (the small
# profile's default memory-store mode, and docker-compose.small.yml's own
# postgres-migrate service already covers the compose case explicitly).
#
# A single migration is allowed to fail without blocking startup: 0008
# onward build toward a not-yet-wired-in "Phase 2" schema (blueprint §22)
# that needs the pg_partman extension, which isn't available on every
# managed Postgres (Render's included). The gateway's actual boot-time
# validation only requires what 0001-0007 create — log-and-continue keeps
# a future migration's missing extension from crash-looping a service that
# doesn't use those tables yet.
set -u

if [ -n "${UBAG_POSTGRES_DSN:-}" ]; then
  for f in /app/migrations/postgres/*.sql; do
    if ! psql "$UBAG_POSTGRES_DSN" -v ON_ERROR_STOP=1 -f "$f"; then
      echo "gateway-entrypoint: WARNING - migration $f failed, continuing" >&2
    fi
  done
fi

exec /app/ubag-gateway
