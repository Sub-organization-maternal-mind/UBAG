---
title: Small Compose Profile
description: Docker Compose scaffolding for the UBAG small deployment profile.
---

The small profile is a single-node Docker Compose deployment scaffold. It builds
the Go gateway from `apps/gateway`, runs Caddy ingress, and provides Postgres,
Dragonfly, MinIO, Prometheus, Grafana, and optional NATS service definitions.

The gateway defaults to in-memory job/idempotency/artifact stores for local
smoke runs, with executor mode set to `noop`. Set `UBAG_GATEWAY_STORE=postgres`
and a matching `UBAG_POSTGRES_DSN` to persist gateway jobs, job events,
worker-event deduplication keys, and idempotency records in Postgres. The
Postgres service loads `migrations/postgres/0001_gateway_stores.sql` and
`migrations/postgres/0002_artifact_metadata.sql` and
`migrations/postgres/0003_webhook_outbox.sql` on first volume initialization;
existing volumes need the relevant migrations applied before switching the
gateway store, MinIO artifact metadata, or webhook outbox to Postgres. Set `UBAG_EXECUTOR_MODE=file`,
`UBAG_EXECUTOR_SPOOL_DIR=/var/lib/ubag/executor-spool`, and
`UBAG_WORKER_CONSUMER_ENABLED=true` to enable the embedded local file-spool
worker consumer. With `UBAG_EXECUTOR_MODE=nats`, the same consumer leases
JetStream jobs through `UBAG_NATS_WORKER_DURABLE`,
`UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`,
`UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`. The
gateway image includes the Python mock worker code used by
the default `UBAG_WORKER_PYTHON` and `UBAG_WORKER_SCRIPT` values. The Compose
profile also provisions data, cache, object storage, queue, and observability
services so operators can validate the full small-profile shape from one
configuration. `UBAG_EXECUTOR_MODE=nats` enables gateway dispatch to NATS
JetStream using `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`.
`UBAG_ARTIFACT_STORE=minio` enables gateway-owned artifact storage and the
`/v1/jobs/{id}/artifacts[/{key}]` routes using `UBAG_MINIO_ENDPOINT`,
`UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and
`UBAG_MINIO_USE_SSL`. `UBAG_WEBHOOK_WORKER_ENABLED=true` enables the signed
webhook delivery worker. The small-profile helper requires
`UBAG_WEBHOOK_OUTBOX=postgres`, `UBAG_WEBHOOK_SECRET`, and a valid
`UBAG_POSTGRES_DSN` before it allows durable webhook delivery.

## Files

| Path | Purpose |
| --- | --- |
| `docker-compose.small.yml` | Root Compose file for the small stack. |
| `deploy/small/env.example` | Placeholder-only environment template. |
| `deploy/small/small.ps1` | PowerShell helper for config, lifecycle, migrations, logs, and smoke checks. |
| `deploy/small/caddy/Caddyfile` | Caddy ingress for `/v1/*` gateway routes. |
| `deploy/small/caddy/Caddyfile.tls.example` | Public-domain Caddy automatic HTTPS example. |
| `deploy/small/prometheus/prometheus.yml` | Prometheus scrape config for Prometheus and gateway `/v1/metrics`. |
| `deploy/small/grafana/provisioning` | Grafana Prometheus datasource provisioning. |

## Start

Create a local env file outside version control and replace every placeholder
value, including `replace-with-local-*` and `set-a-local-*`:

```powershell
Copy-Item deploy\small\env.example deploy\small\env.local
notepad deploy\small\env.local
```

Render the Compose config:

```powershell
.\deploy\small\small.ps1 -Action config
```

`config` renders from `deploy/small/env.example` by default, even when
`env.local` exists, so local secrets are not printed accidentally. Pass
`-AllowSecretConfigOutput` only when rendered `env.local` values are explicitly
needed. `up`, `smoke`, and `migrate` require `deploy/small/env.local` with placeholder
values replaced.

Start the core stack:

```powershell
.\deploy\small\small.ps1 -Action up
```

Check the gateway directly:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/health
```

Check the same route through Caddy:

```powershell
Invoke-RestMethod http://127.0.0.1:8081/v1/health
```

Run the direct gateway health/readiness, Caddy ingress health, and mock-worker
smoke path:

```powershell
.\deploy\small\small.ps1 -Action smoke
```

Apply or re-apply Postgres migrations to an existing volume:

```powershell
.\deploy\small\small.ps1 -Action migrate
```

For durable gateway state in the small profile, set `UBAG_GATEWAY_STORE` to
`postgres` in `deploy/small/env.local`. `/v1/ready` fails until Postgres is
reachable and the gateway tables are present. Fresh volumes load both Postgres
migrations plus `0003_webhook_outbox.sql`. Existing volumes must apply
`0002_artifact_metadata.sql` manually before enabling `UBAG_ARTIFACT_STORE=minio`
with Postgres metadata, and `0003_webhook_outbox.sql` before enabling
`UBAG_WEBHOOK_OUTBOX=postgres` or the webhook worker. The `migrate` action runs
all current Postgres migration files with `ON_ERROR_STOP=1`.

Enable NATS dispatch:

```powershell
UBAG_EXECUTOR_MODE=nats
UBAG_NATS_URL=nats://nats:4222
UBAG_NATS_STREAM=UBAG_JOBS
UBAG_NATS_SUBJECT=ubag.jobs
UBAG_WORKER_CONSUMER_ENABLED=true
UBAG_NATS_WORKER_DURABLE=ubag-worker
UBAG_NATS_WORKER_ACK_WAIT_MS=35000
UBAG_NATS_WORKER_NAK_DELAY_MS=1000
UBAG_NATS_WORKER_FETCH_WAIT_MS=500
UBAG_NATS_WORKER_MAX_DELIVER=5
.\deploy\small\small.ps1 -Action up -Profile queue
```

Enable MinIO artifact storage:

```powershell
UBAG_ARTIFACT_STORE=minio
UBAG_MINIO_ENDPOINT=minio:9000
UBAG_MINIO_ACCESS_KEY=ubag-gateway
UBAG_MINIO_SECRET_KEY=<local-minio-gateway-password>
UBAG_MINIO_BUCKET=ubag-artifacts
UBAG_MINIO_USE_SSL=false
```

The Compose stack starts `minio-init` after MinIO is healthy. It creates the
artifact bucket and a least-privilege `ubag-artifacts-rw` policy for the gateway
access key. Keep `MINIO_ROOT_USER` and `MINIO_ROOT_PASSWORD` separate from
`UBAG_MINIO_ACCESS_KEY` and `UBAG_MINIO_SECRET_KEY`.

Enable signed webhook delivery:

```powershell
UBAG_GATEWAY_STORE=postgres
UBAG_POSTGRES_DSN=postgres://ubag:<local-postgres-password>@postgres:5432/ubag?sslmode=disable
UBAG_WEBHOOK_OUTBOX=postgres
UBAG_WEBHOOK_WORKER_ENABLED=true
UBAG_WEBHOOK_SECRET=<local-webhook-signing-secret>
UBAG_WEBHOOK_MAX_ATTEMPTS=8
UBAG_WEBHOOK_POLL_INTERVAL_MS=1000
UBAG_WEBHOOK_BATCH_SIZE=10
UBAG_WEBHOOK_LEASE_MS=30000
UBAG_WEBHOOK_REQUEST_TIMEOUT_MS=10000
UBAG_WEBHOOK_RETRY_BASE_MS=1000
UBAG_WEBHOOK_RETRY_MAX_MS=300000
UBAG_WEBHOOK_ALLOWED_HOSTS=example.com
UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=false
```

The webhook worker requires an outbound host allowlist in shared/small runs.
Only set `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true` after explicit outbound SSRF
review; URL validation and delivery still reject private, local, link-local, and
metadata addresses.

## Optional Profiles

Start observability:

```powershell
.\deploy\small\small.ps1 -Action up -Profile observability
```

Prometheus is available on `http://127.0.0.1:9090`; Grafana is available on
`http://127.0.0.1:3000`. The Prometheus config scrapes Prometheus and the
gateway `/v1/metrics` endpoint; Caddy admin stays bound to localhost inside
its container.

For public-domain Caddy automatic HTTPS, keep backing service ports on loopback,
bind only the edge ingress externally, and point Caddy at the TLS example:

```powershell
UBAG_EDGE_BIND_HOST=0.0.0.0
UBAG_CADDY_HTTP_PORT=80
UBAG_CADDY_HTTPS_PORT=443
UBAG_CADDYFILE=./deploy/small/caddy/Caddyfile.tls.example
UBAG_PUBLIC_DOMAIN=ubag.example.com
```

Start optional NATS JetStream:

```powershell
.\deploy\small\small.ps1 -Action up -Profile queue
```

Stop the stack:

```powershell
.\deploy\small\small.ps1 -Action down
```
