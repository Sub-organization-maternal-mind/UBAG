#!/bin/sh
# Applies migrations/postgres/*.sql before starting the gateway, then execs it.
# Render's Free plan doesn't support preDeployCommand/one-off jobs, so this
# runs on every container start instead - every migration is written to be
# idempotent, so re-running is a no-op once applied.
#
# No-ops entirely when UBAG_POSTGRES_DSN is unset (the small profile's default
# memory-store mode; docker-compose.small.yml's own postgres-migrate service
# covers the compose case explicitly).
#
# FAIL-CLOSED. A migration that does not apply cleanly stops the boot. This is
# deliberate: the previous implementation logged a warning and continued, which
# made a half-applied schema the EXPECTED outcome rather than a visible failure.
# The gateway's readiness probe checks connectivity and a couple of specific
# tables, so a migration that failed half-way through could leave the service
# reporting ready while the tables its other code paths need do not exist.
#
# OPTIONAL MIGRATIONS. 0008_blueprint_schema.sql requires the pgvector and
# pg_partman extensions, which are not available on every managed Postgres
# (Render's included). Because psql runs with ON_ERROR_STOP=1, a missing
# extension aborts that file at line 18 and NONE of its tables are created -
# which is safe here, because 0008 is a not-yet-wired-in "Phase 2" schema
# (blueprint section 22) that the gateway does not read: it targets
# automation_jobs / webhook_endpoints / prompt_templates / app_credentials,
# while the live code uses gateway_jobs / gateway_webhook_deliveries.
# It is therefore SKIPPED unless UBAG_ALLOW_OPTIONAL_MIGRATIONS=1, and when
# it IS enabled it is fail-closed like every other migration. To adopt those
# tables, install the extensions and flip the flag deliberately.
#
# TRANSACTIONAL + LOCKED. The whole run is a single psql invocation with
# --single-transaction: every mandatory migration commits together or not at
# all, so a failure can no longer leave a half-applied schema behind (psql
# prefixes errors with the failing file's path, so the culprit stays visible).
# The transaction opens with pg_advisory_xact_lock(hashtext('ubag-migrations')),
# so two gateways booting concurrently (Render scale-up, rolling deploys) or a
# concurrent `ubag db-migrate` (which takes the same lock per file) serialize
# instead of interleaving DDL. The lock is transaction-scoped: it is released
# at COMMIT/ROLLBACK with no unlock step and no leak if the process dies.
set -eu

# Extension-dependent migrations, skipped unless explicitly enabled.
OPTIONAL_MIGRATIONS="0008_blueprint_schema.sql"

if [ -n "${UBAG_POSTGRES_DSN:-}" ]; then
  allow_optional="${UBAG_ALLOW_OPTIONAL_MIGRATIONS:-0}"

  # Build the psql argument list (one -f <file> pair per mandatory migration,
  # in filename order).
  applied=0
  skipped=0
  set --
  for f in /app/migrations/postgres/*.sql; do
    name=$(basename "$f")

    is_optional=0
    for opt in $OPTIONAL_MIGRATIONS; do
      if [ "$name" = "$opt" ]; then
        is_optional=1
        break
      fi
    done

    if [ "$is_optional" -eq 1 ] && [ "$allow_optional" != "1" ]; then
      echo "gateway-entrypoint: skipping optional migration $name (set UBAG_ALLOW_OPTIONAL_MIGRATIONS=1 after installing pgvector + pg_partman to enable)" >&2
      skipped=$((skipped + 1))
      continue
    fi

    echo "gateway-entrypoint: staging $name" >&2
    set -- "$@" -f "$f"
    applied=$((applied + 1))
  done

  if [ "$applied" -gt 0 ]; then
    echo "gateway-entrypoint: applying $applied migration(s) in one transaction under the ubag-migrations advisory lock" >&2
    # ON_ERROR_STOP=1 makes psql exit non-zero on the first error; the
    # --single-transaction wrapper then rolls the whole run back instead of
    # committing a partially applied schema.
    if ! psql "$UBAG_POSTGRES_DSN" --single-transaction -v ON_ERROR_STOP=1 \
        -c "SELECT pg_advisory_xact_lock(hashtext('ubag-migrations'))" "$@"; then
      echo "gateway-entrypoint: FATAL - a migration failed; refusing to start with a partially applied schema." >&2
      echo "gateway-entrypoint: fix the migration (or the database) and restart. To skip optional migrations see the header of this script." >&2
      exit 1
    fi
  fi

  echo "gateway-entrypoint: migrations complete (applied=$applied skipped=$skipped)" >&2
fi

exec /app/ubag-gateway
