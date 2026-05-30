# Postgres round-trip integration tests

The Go gateway ships native Postgres stores for the small/enterprise deployment
profile. Their integration tests are **env-gated**: each test skips itself when
`UBAG_TEST_POSTGRES_DSN` is unset, so a plain `go test ./...` (or
`pnpm test:gateway`) passes without ever touching Postgres.

`tools/run-postgres-roundtrip-tests.mjs` runs those tests against a **real**
Postgres instance and fails loudly if the database was never reached, so a green
result actually proves the round trip.

## What it covers

The runner discovers and runs every gateway package that carries a
Postgres-gated test:

- `internal/artifacts` (artifact metadata; MinIO path stays separately gated)
- `internal/httpapi` (webhook secret store)
- `internal/idempotency`
- `internal/jobs`
- `internal/ratelimit`
- `internal/responsecache`
- `internal/scim`
- `internal/siem`
- `internal/sso`
- `internal/webhooks`
- `internal/workflow`

## Prerequisites

- A reachable Postgres instance.
- The schema applied from `migrations/postgres/0001..0007_*.sql` (all idempotent
  `CREATE ... IF NOT EXISTS`).
- Go available on PATH or via the local Codex toolchain (same discovery as
  `tools/run-go-tests.mjs`).
- `psql` on PATH **only** if you use `--apply-migrations`.

## Quick start (local small profile)

```powershell
# 1. Start the bundled Postgres
docker compose -f docker-compose.small.yml up -d postgres

# 2. Point the runner at it
$env:UBAG_TEST_POSTGRES_DSN = "postgres://ubag:ubag@localhost:5432/ubag?sslmode=disable"

# 3. Apply migrations and run the round-trip suite
node tools/run-postgres-roundtrip-tests.mjs --apply-migrations
```

Or via the workspace script (migrations assumed already applied):

```powershell
pnpm test:gateway:postgres
```

## Flags / environment

| Name | Purpose |
| --- | --- |
| `UBAG_TEST_POSTGRES_DSN` | **Required.** pgx-style DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=disable`. |
| `--apply-migrations` | Applies `migrations/postgres/*.sql` in order via `psql` before testing. Idempotent. |

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Postgres tests ran and passed against the live database. |
| `1` | DSN missing, Go unavailable, migration failure, or a test failed. |
| `2` | DSN was set but every Postgres test skipped — the database was never reached (false-green guard). |

## Safety notes

- Migrations only **add** objects; the runner never drops or truncates anything.
- The Go tests scope their writes to throwaway tenant/app identifiers.
- Nothing here ingests credentials or performs ToS-restricted automation.
