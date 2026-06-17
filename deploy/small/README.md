# UBAG Small Profile

This profile is Docker Compose scaffolding for a single-node UBAG deployment.
It builds the current Go gateway, runs local infrastructure services, and keeps
all shared credentials outside the repository.

The gateway defaults to in-memory job/idempotency/artifact stores plus a `noop`
executor for local smoke runs. Set `UBAG_GATEWAY_STORE=postgres` and a matching
`UBAG_POSTGRES_DSN` to persist gateway jobs, job events, worker-event
deduplication keys, and idempotency records in Postgres. The Postgres service
loads `migrations/postgres/0001_gateway_stores.sql` and
`migrations/postgres/0002_artifact_metadata.sql` and
`migrations/postgres/0003_webhook_outbox.sql` when its volume is first
initialized; existing volumes must be migrated manually before switching the
gateway store, MinIO artifact metadata, or webhook outbox to Postgres. Set `UBAG_EXECUTOR_MODE=file`,
`UBAG_EXECUTOR_SPOOL_DIR=/var/lib/ubag/executor-spool`, and
`UBAG_WORKER_CONSUMER_ENABLED=true` to run the embedded local file-spool
consumer. With `UBAG_EXECUTOR_MODE=nats`, the same embedded consumer leases
JetStream messages through `UBAG_NATS_WORKER_DURABLE`,
`UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`, and
`UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`. The gateway image includes the Python mock worker path used by
`UBAG_WORKER_PYTHON=/usr/bin/python3` and
`UBAG_WORKER_SCRIPT=/app/apps/worker/run_mock_worker.py`. Postgres, Dragonfly,
MinIO, NATS, Prometheus, and Grafana are provided as the small-profile backing
shape. `UBAG_EXECUTOR_MODE=nats` enables gateway dispatch to NATS JetStream
through `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`.
`UBAG_ARTIFACT_STORE=minio` enables gateway-owned artifact upload/list/download
and delete routes backed by MinIO via `UBAG_MINIO_ENDPOINT`,
`UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and
`UBAG_MINIO_USE_SSL`. `UBAG_WEBHOOK_WORKER_ENABLED=true` enables the signed
webhook delivery worker. Use `UBAG_WEBHOOK_OUTBOX=postgres`,
`UBAG_WEBHOOK_SECRET`, retry timing variables, and the URL policy variables
before enabling it in shared small-profile environments.

## Files

- `docker-compose.small.yml`: root Compose file for the small stack.
- `deploy/small/env.example`: placeholder environment template with no secrets.
- `deploy/small/small.ps1`: PowerShell helper for config, up, down, logs, and smoke checks.
- `deploy/small/nginx-dashboard/default.conf.template`: local nginx ingress for `/dashboard/*`, `/v1/*`, and `/novnc/*`.
- `deploy/small/prometheus/prometheus.yml`: Prometheus and gateway `/v1/metrics` scrape config.
- `deploy/small/grafana/provisioning`: Grafana datasource provisioning.
- `deploy/small/*Dockerfile`: gateway and mock-worker build contexts. The gateway image carries the mock worker code so the embedded consumer can be enabled for local file-spool runs.

## Local Start

Create a local environment file and replace every placeholder value, including
`replace-with-local-*` and `set-a-local-*`:

```powershell
Copy-Item deploy\small\env.example deploy\small\env.local
notepad deploy\small\env.local
```

Validate the rendered Compose config:

```powershell
.\deploy\small\small.ps1 -Action config
```

The helper renders `config` from `env.example` by default, even when
`env.local` exists, so local secrets are not printed accidentally. Pass
`-AllowSecretConfigOutput` only when you intentionally need rendered
`env.local` values. The helper refuses `up`, `smoke`, and `migrate` until
`deploy/small/env.local` exists and placeholder values have been replaced.

Start the core small stack:

```powershell
.\deploy\small\small.ps1 -Action up
```

Check gateway health directly:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/health
```

Check nginx-dashboard ingress:

```powershell
Invoke-RestMethod http://127.0.0.1:8083/v1/health
```

Run the direct gateway health/readiness, nginx-dashboard ingress health, and mock-worker
smoke path:

```powershell
.\deploy\small\small.ps1 -Action smoke
```

The smoke still includes the standalone mock JSONL worker path. Gateway
file-spool dispatch, leasing, cancellation, and result ingestion are covered by
`cmd /c pnpm test:gateway` and can be enabled in Compose with the worker
consumer variables above.

Apply or re-apply Postgres migrations to an existing volume:

```powershell
.\deploy\small\small.ps1 -Action migrate
```

For durable gateway state in the small profile, set `UBAG_GATEWAY_STORE` to
`postgres` in `deploy/small/env.local` and keep `UBAG_POSTGRES_DSN` aligned
with the Postgres credentials in the same file. `/v1/ready` fails until the
Postgres service is reachable and the gateway tables are present. Fresh volumes
load `0001_gateway_stores.sql`, `0002_artifact_metadata.sql`, and
`0003_webhook_outbox.sql`. Existing volumes must apply the second migration
explicitly before enabling `UBAG_ARTIFACT_STORE=minio` with Postgres metadata,
and the third migration before enabling `UBAG_WEBHOOK_OUTBOX=postgres` or the
webhook worker. The `migrate` action runs the same migration files against the
current Postgres service with `ON_ERROR_STOP=1`.

Enable NATS dispatch:

```powershell
# in deploy\small\env.local
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
# in deploy\small\env.local
UBAG_ARTIFACT_STORE=minio
UBAG_MINIO_ENDPOINT=minio:9000
UBAG_MINIO_ACCESS_KEY=ubag-gateway
UBAG_MINIO_SECRET_KEY=<local-minio-gateway-password>
UBAG_MINIO_BUCKET=ubag-artifacts
UBAG_MINIO_USE_SSL=false
```

The Compose stack starts `minio-init` after MinIO is healthy. It creates the
artifact bucket and a least-privilege `ubag-artifacts-rw` policy for the gateway
access key. `MINIO_ROOT_USER` and `MINIO_ROOT_PASSWORD` are used only for this
bootstrap/admin path; do not reuse them as `UBAG_MINIO_ACCESS_KEY` or
`UBAG_MINIO_SECRET_KEY`.

Enable signed webhook delivery:

```powershell
# in deploy\small\env.local
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

Keep `UBAG_WEBHOOK_ALLOWED_HOSTS` non-empty when the worker is enabled. Only
set `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true` after an explicit outbound SSRF
review; DNS and connect-time checks still block private, local, link-local, and
metadata addresses.

Stop the stack:

```powershell
.\deploy\small\small.ps1 -Action down
```

## Optional Profiles

Start observability services:

```powershell
.\deploy\small\small.ps1 -Action up -Profile observability
```

Grafana listens on `http://127.0.0.1:3000` and Prometheus listens on
`http://127.0.0.1:9090` by default. Prometheus scrapes itself and gateway
`/v1/metrics`.

`UBAG_EDGE_BIND_HOST` controls nginx-dashboard ingress. `UBAG_BACKING_BIND_HOST` controls
gateway, Postgres, Dragonfly, MinIO, Prometheus, Grafana, and NATS host ports
and defaults to loopback. Keep `UBAG_BACKING_BIND_HOST` on loopback unless a
firewall review explicitly approves public backing-service ports. For public
deployments, bind only the edge ingress externally and keep backing services
private.

For public-domain TLS, terminate HTTPS at an external reverse proxy or replace
the edge service with the standard Caddy profile after the public-domain Caddy
configuration is wired for the target host. Keep backing service ports on
loopback and bind only the edge ingress externally:

```powershell
# in deploy\small\env.local
UBAG_EDGE_BIND_HOST=0.0.0.0
UBAG_NGINX_HTTP_PORT=80
UBAG_PUBLIC_DOMAIN=ubag.example.com
```

Start the optional NATS JetStream service:

```powershell
.\deploy\small\small.ps1 -Action up -Profile queue
```

### Live-browser viewer (noVNC)

The `live-browser` profile adds a `browser-viewer` service: a real, persistent
Chromium running on a virtual display (Xvfb), streamed to the operator's browser
over noVNC (`x11vnc` + `websockify`). It exists so a **human** can complete
login, CAPTCHA, 2FA, and consent prompts in a user-owned session. UBAG never
fills credentials, captures cookies or storage state, or solves challenges — the
machine only attaches to the already-logged-in profile over CDP to run jobs.

```powershell
# in deploy\small\env.local
UBAG_BROWSER_VNC_PASSWORD=choose-a-strong-vnc-password
UBAG_REMOTE_BROWSER_ENDPOINT=http://browser-viewer:9222
UBAG_NOVNC_BASE_URL=http://127.0.0.1:7900
```

```powershell
docker compose --env-file deploy\small\env.local -f docker-compose.small.yml --profile live-browser up -d --build
```

Security posture:

- noVNC is published to loopback only (`UBAG_NOVNC_PORT`, default `7900`) and is
  password-gated by `UBAG_BROWSER_VNC_PASSWORD`. Reach it over an SSH tunnel or
  through the edge route `/novnc/` (the dashboard's **Take control** button).
- Chromium DevTools/CDP (`9222`) stays on the internal `ubag-private` network and
  is never published to a host port.
- The browser profile persists on the `browser_profiles` volume so manual logins
  survive restarts. Place that volume on an encrypted disk for shared hosts.
- The gateway only forwards `novnc_url` values that are loopback URLs, so keep
  `UBAG_NOVNC_BASE_URL` on `127.0.0.1`/`localhost`.

Run raw Compose commands without the helper:

```powershell
docker compose --env-file deploy\small\env.local -f docker-compose.small.yml up -d --build
docker compose --env-file deploy\small\env.local -f docker-compose.small.yml --profile observability up -d
docker compose --env-file deploy\small\env.local -f docker-compose.small.yml down
```
