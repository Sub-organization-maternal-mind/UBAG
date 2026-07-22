# UBAG Agent Handoff

Last updated: 2026-07-23

This is the resume point for any future agentic AI working in `D:\Projects\UBAG`.
Read this file first, then `PROGRESS.md`, then `IMPLEMENTATION_COVERAGE.md`.

## Current Repository State

- Working directory: `D:\Projects\UBAG`.
- Git is initialized on branch `main`, tracking `origin/main`.
- Preserve `AGENTS.md`, `design.md`, `.codex`, and all current workspace contents.
- Do not run `git reset`, `git clean`, or destructive checkout commands unless the user explicitly asks.

## Latest Slice: Gemini 3.6 Standard + source synchronization (2026-07-23)

- Local `main` was fast-forwarded by 109 commits to GitHub `origin/main` at `9da31f5`; the full pre-sync dirty tree remains recoverable in `stash@{0}` (`codex-pre-sync-2026-07-23-local-and-gemini36`).
- Production `/opt/docker/ubag` was compared to GitHub source-only. Runtime logs, spool records, databases, generated binaries, `.htpasswd`, and `deploy/vps/env.local` were excluded. Production's worker engine/page driver match GitHub; only the Gemini selector policy was newer and eligible to promote.
- Gemini now enforces model `3.6 Flash` and treats Standard thinking as `Extended thinking = false`. The flattened Gemini picker persists these selections independently.
- Shared commit `acec1ed` was pushed to GitHub `main`, and all 1,121 tracked files were synchronized into production with zero missing files and zero hash mismatches.
- Production gateway image `sha256:dde174b3d9422bba95c4022c753171f7b0ae830a3617acec6a798875eec52559` is healthy. Final live job `job_000000000029` completed with exact output `UBAG_SYNCED_GEMINI_36_STANDARD_OK` and selector version `2026-07-23-gemini-3.6-standard`.

## Latest Slice: Orchestration Semantics (2026-07-16)

Per-request model/mode selection + conversation affinity landed across contracts, gateway, worker, and SDK/CLI/dashboard, inert by default behind `UBAG_CONVERSATIONS_ENABLED` (default false). See the 2026-07-16 section of `PROGRESS.md` for the full description and the design/plan under `docs/superpowers/`.

Key runtime facts for the next agent:

- New env flag `UBAG_CONVERSATIONS_ENABLED` (default false). When enabled, the store backend follows the existing `UBAG_GATEWAY_STORE` `storeKind` (memory/sqlite/postgres), exactly like alerts. Postgres requires applying `migrations/postgres/0010_conversations.sql` (readiness fails closed otherwise); SQLite self-bootstraps.
- New route `GET /v1/conversations` (`job:read`, nil-safe 501 when disabled).
- `job.model_settings` is a flat map keyed by each adapter's own `ProviderSetting.key`; the gateway validates it against the adapter manifest `model_catalog` and copies it into the worker envelope `options.provider_config`. Client-supplied `options.provider_config` is stripped at create time.
- Worker emits `conversation.thread_bound/_broken/_rebound` with a **flat** top-level `thread_ref` (chat URL only) — the gateway `WorkerConsumer` reads `data.thread_ref` non-recursively; keep any new emitter flat.
- Next roadmap slices (not started): provider expansion (Kimi/Minimax/Claude activation), automatic provider fallback/routing, mobile push alerting.
- Follow-ups: promote the dashboard `/conversations` page into the sidebar nav (requires updating the §24.2 17-page inventory + the e2e count); add typed `model_settings` to gRPC/proto if a typed non-HTTP surface is wanted.

Host note: bare `python` on the current Windows host resolves to a broken Store alias stub; use `C:\Users\Admin\AppData\Local\Python\bin\python.exe` with `PYTHONPATH="apps/worker;adapters/mock"`. The single gateway test `TestProcessWorkerRunnerRunsPythonWorkerFromGatewayEnvelope` fails only for this alias reason.

## Current Product Phase

The repository has completed the docs-first Milestone 0 baseline and the v0 edge foundation slice.

Current implemented or validateable scope:

- Astro Starlight documentation site under `apps/docs`.
- Root planning and tracking docs: `PRD.md`, `PROGRESS.md`, `IMPLEMENTATION_COVERAGE.md`, and this handoff.
- OpenAPI, shared JSON Schemas, Protobuf seed contracts, SDK fixtures, and contract checks.
- Conformance fixtures currently include 41 executable REST scenarios plus 272 named non-executable coverage scenarios.
- Go gateway with `/v1` health, readiness, version, metrics, jobs, tenant/app-scoped cross-job events, SSE, WebSocket upgrade guard, workflows, built-in template catalog/application, targets/adapters, apps, devices, webhooks, cache status, audit, cancel, retry, stable errors, idempotency, idempotent artifact mutations, paginated operator collections, and app-secret auth.
- Gateway-side executable payload safety checks, internal executor dispatch boundary, and optional embedded worker consumer/result ingestion for local file-spool and NATS JetStream leases.
- Opt-in Postgres gateway stores for jobs, events, worker-event dedupe keys, and idempotency records via `UBAG_GATEWAY_STORE=postgres`.
- Edge queue and SQLite/localfs-oriented storage contracts plus migrations and conformance checks; gateway runtime persistence is memory by default and Postgres/MinIO when configured.
- Python worker, deterministic mock adapter, safe-mode provider manifests, manual-session events, artifact policies, and secret-material rejection.
- Safe-mode adapter coverage for DeepSeek, ChatGPT, Claude, Gemini, Mistral, Perplexity, generic chat, generic form, and mock.
- TypeScript/JavaScript and Go SDK wave with generated operation-level contract-manifest freshness checks for system, job, job-event, artifact list/upload/download/delete, operator collection, webhook replay, workflow/template list, cache, apps/devices/audit, metrics, and stream entrypoint endpoints.
- TypeScript CLI with health/ready/version, diagnose, create/get/list/cancel/retry, event/artifact/operator/webhook/cache/metrics commands, SSE streaming, mock-run, and adapter-test coverage.
- Loopback sidecar with `/health`, `/v1/*` proxy, mutating-route idempotency generation including artifact PUT/DELETE, and public-binding guard.
- NAJM/Hallmark operator dashboard under `apps/dashboard`, wired to gateway APIs with local fixtures only for tests and empty/offline states. The dashboard consumes gateway-native browser topology fields, embeds only runtime-generated loopback noVNC URLs, renders template preview output from the gateway `rendered` field, and does not invent workflow DAGs when the list endpoint only returns metadata.
- Security/compliance contracts for app-secret auth, device tokens, RBAC/ABAC, audit redaction/chaining, webhook signing, and rate-limit decisions.
- Observability package with metric, event, log, health-probe, and smoke-check registries.
- Small deployment profile with Docker Compose, nginx-dashboard ingress, Postgres, Dragonfly, MinIO, Prometheus/Grafana, and optional NATS.
- NATS JetStream gateway dispatch and embedded durable worker consumption are implemented via `UBAG_EXECUTOR_MODE=nats`, `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, `UBAG_NATS_SUBJECT`, and the `UBAG_NATS_WORKER_*` settings.
- MinIO artifact storage is implemented via `UBAG_ARTIFACT_STORE=minio`, `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL`, with Postgres metadata in `migrations/postgres/0002_artifact_metadata.sql` when the gateway store is Postgres-backed.
- Signed webhook outbox delivery is implemented via per-job callback config, `UBAG_WEBHOOK_OUTBOX`, Postgres migration `migrations/postgres/0003_webhook_outbox.sql`, HMAC signing secrets from environment, strict callback URL policy, and an opt-in retry worker controlled by `UBAG_WEBHOOK_WORKER_ENABLED`.
- Built-in template catalog/runtime foundation is implemented in memory: `/v1/templates` lists built-ins, readiness verifies the template store, and job creation applies template defaults before payload policy validation, storage, idempotency hashing, and executor enqueue.
- The latest gateway completion sweep hardened secret-like payload key detection, replaced the `/v1/events` placeholder with real scoped event listing, added collection pagination/AuthZ, required idempotency for artifact PUT/DELETE replay, aligned the Mistral adapter catalog key as `mistral_lechat`, and added proto/OpenAPI/schema lint command coverage.
- The latest hardening pass closed repo-local audit gaps in template-default job creation, callback secret-reference handling, manual-session event data preservation, sidecar artifact idempotency, SDK/CLI endpoint parity, dashboard CSP/state coverage, small-profile public ingress guards, Postgres migration reruns, MinIO least-privilege bootstrap, nginx-dashboard ingress, gateway graceful shutdown, observability readiness/smoke probes, contract drift, and docs claim accuracy.
- The 2026-05-29 pass added a runtime SQLite/localfs persistence path and six enterprise leaf packages to the gateway, all code-complete and locally validated with green `go build`/`vet`/`test ./...` (gRPC + grpc-web were completed in a previous slice):
  - Runtime stores: `UBAG_GATEWAY_STORE=sqlite` (WAL, `busy_timeout`, `foreign_keys`, single-writer), `UBAG_ARTIFACT_STORE=localfs` with `UBAG_ARTIFACT_DIR`, and a SQLite webhook outbox mode.
  - `internal/ratelimit` (memory + SQLite + Postgres stores, policy resolver), `internal/responsecache` (memory + SQLite, never exposes cached payload values), `internal/workflow` (memory + SQLite multi-step runs with payload policy on every step input), `internal/sso` (stdlib OIDC RS256 + SAML verification, memory + SQLite config store), `internal/scim` (SCIM v2 Users/Groups CRUD+Patch, memory + SQLite, passwords never stored), and `internal/siem` (redacted audit/event export via File/HTTP/Syslog sinks with a non-blocking exporter).
  - `internal/httpapi` wiring is nil-safe/optional so unconfigured subsystems leave existing behavior unchanged; new routes and RBAC actions are `GET /v1/cache` (`job:read`) and `DELETE /v1/cache` (`rate_limit:manage`), `GET /v1/rate-limits` (`rate_limit:manage`), `GET/POST /v1/workflows` + `POST /v1/workflows/{id}/runs` + `GET /v1/workflows/runs/{id}` (`job:read`/`job:create`), `GET/PUT /v1/sso/config` (`role:manage`) + `POST /v1/sso/oidc/callback` + `POST /v1/sso/saml/acs` (verification, no RBAC), `/v1/scim/v2/Users[/{id}]` and `/v1/scim/v2/Groups[/{id}]` (`role:manage`), `GET/PUT /v1/siem/config` (`role:manage`) + `POST /v1/audit/export` (`data:export`), `POST /v1/webhooks/secret:rotate` (`secret:rotate`), and a `withRateLimit` middleware that is pass-through when disabled.
  - New env vars: `UBAG_RATE_LIMIT_ENABLED` (default false), `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`, `UBAG_SIEM_FILE_PATH`.
  - Independent review PASSED with no Critical/High findings; two hardening fixes were applied (cache purge returns `501` when disabled; SSO config `PUT` rejects OIDC without an Issuer and SAML without an IdP certificate).
  - SDK limitation: active first-class SDK support is TypeScript/JavaScript and Go only. Prior Rust, Python, Java, Kotlin, Ruby, PHP, C#, Swift, and Elixir SDK package trees are no longer part of active CI, docs, packaging, or release claims.

## Subagent Audit Closure

The initial v0 baseline closed six parallel review workstreams. Later implementation slices added further parallel reviews for Postgres gateway stores, NATS/MinIO, NATS worker consumption, signed webhook outbox delivery, and the 2026-05-24 completion sweep. The detailed evidence chain and subagent counts are tracked in `PROGRESS.md`; this handoff records only the current resume state.

## Latest Known Green Validation

After the 2026-06-17 TS+Go-only SDK completion pass, the following validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:sdk:typescript
cmd /c pnpm test:sdk:go
cmd /c pnpm test:sdk
node packages/conformance/scripts/validate-fixtures.mjs
cmd /c pnpm test:worker
cmd /c pnpm test:dashboard
cmd /c pnpm test:deployment
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

Docker Compose was not installed on this host, so `test:deployment` used its
static deployment checks after explicitly reporting the compose-render skip.
The supported SDK set is TypeScript/JavaScript (`@ubag/sdk`) and Go
(`github.com/ubag/ubag-go`) only.

After the 2026-06-18 dashboard-only completion pass, the following validation passed:

```powershell
cmd /c pnpm --filter @ubag/dashboard check
cmd /c pnpm --filter @ubag/dashboard test
cmd /c pnpm --filter @ubag/dashboard test:e2e
cmd /c pnpm test:dashboard
```

After the 2026-06-18 production live-browser activation, `ubag.polytronx.com`
uses the `live-browser` profile with a production `browser-topology-register`
service. The registrar idempotently upserts one Chromium instance and three
provider contexts/tabs (`chatgpt_web`, `gemini_web`, `deepseek_web`) for
`tenant_edge`, so Browser Sessions should survive restarts/redeploys without
manual database inserts. Production verification returned 1
`gateway_browser_instances` row, 3 `gateway_provider_contexts` rows, and 3
joined `gateway_browser_tabs` rows. Gemini and DeepSeek were operator-login
checked; ChatGPT remains manual-login pending. Do not read provider cookies,
storage state, credentials, or production secret files while validating this
flow.

After the 2026-06-18 production operator activation pass, production also runs
`browser-topology-sync` under the `live-browser` profile. It reruns the same
idempotent registration every `UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS` seconds, so
Browser Sessions should repopulate automatically after restarts without manual
DB inserts. The production dashboard bundle now includes:

- Jobs page submitter for `chatgpt_web`, `gemini_web`, and `deepseek_web` using
  the real `/v1/jobs` envelope.
- Correct job cancel/retry routes (`/v1/jobs/{id}/cancel`,
  `/v1/jobs/{id}/retry`).
- Workflows page create/run controls using `/v1/workflows` and
  `/v1/workflows/{id}/runs`.
- Workflows page ordered-chain mode that creates steps in this provider order:
  ChatGPT, Gemini, DeepSeek. It keeps single-provider mode available and shows
  live provider readiness from `/v1/browser/contexts`.

Production smoke evidence: `https://ubag.polytronx.com/dashboard/jobs/`,
`/dashboard/workflows/`, and `/dashboard/browser/` rendered in headless Chrome;
browser topology showed 1 instance, 3 contexts, 3 tabs; safe mock job
`job_000000000001` was accepted/queued; safe mock workflow
`wfd_6d78879ffd80099234a51848` ran successfully as
`wfr_e198f2fa93daa73b20f1a810` with `job_000000000002`. No failed jobs existed
at inspection time. A follow-up smoke confirmed the ordered workflow UI renders
the requested chain and provider readiness states. External `/v1/ready` is
intentionally blocked by nginx; use external `/v1/health` and internal container
healthchecks for readiness.

After the 2026-06-01 worker runtime orchestration integration (Option A, full), the following validation was green (all exit 0):

```powershell
node tools/run-go-tests.mjs apps/gateway        # all packages ok (executor re-ran with new topology tests)
node tools/run-python-worker-tests.mjs          # 143 tests (122 legacy + 21 new) + 5 + smoke, EXIT=0
cmd /c pnpm check
cmd /c pnpm test:v0
```

The live worker now optionally routes jobs through `LiveOrchestrator` (Fleet + per-(tenant,provider,identity) ChannelPool with persistent AIMD) and emits `browser.topology_reported` + `concurrency.cap_changed`; the gateway `WorkerConsumer` projects topology snapshots into its in-memory `topology.MemoryStore` (tenant-forced, storage-state redacted, poison-safe intercept). The integration is opt-in (`orchestrator=None` and a nil `Topology` ingestor keep the legacy path byte-identical), so all pre-existing tests stay green. The live real-browser provider path is still ToS-bound and cannot be CI-validated; all new wiring is validated via offline/mock drivers, fakes, and unit/structure tests only.

After the 2026-05-29 gateway runtime-stores and enterprise-surface pass, the following gateway validation was green on the Go 1.26 toolchain (all `apps/gateway` code-complete and locally validated):

```powershell
go build ./...
go vet ./...
go test ./...
cmd /c pnpm test:plugins
cmd /c pnpm test:adapter-registry
cmd /c pnpm test:v0:local
```

`test:plugins` reports 20/20 and `test:adapter-registry` reports 16/16. The new SQLite/localfs runtime stores and the `ratelimit`/`responsecache`/`workflow`/`sso`/`scim`/`siem` packages each ship with passing Go package tests; live multi-process durability and Postgres-native stores for the non-rate-limiter subsystems remain follow-ups.

After the 2026-05-25 continuation hardening pass, the following sequential validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:deployment
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git --no-pager diff --check
```

The following commands passed after the v0 implementation and subagent fix pass:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:deployment
cmd /c pnpm check:contracts
cmd /c pnpm check:sdk-freshness
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the 2026-05-24 hardening pass, the following validation passed:

```powershell
cmd /c pnpm check:docs-responsive
cmd /c pnpm test:dashboard
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

`cmd /c pnpm test:v0` passed after responsive verifiers were moved to OS-assigned local ports and page-readiness polling so concurrent or stale local preview servers no longer affect the checks.

`cmd /c pnpm test:v0` includes schema, edge-store, security, worker, sidecar, SDK, conformance, observability, CLI, dashboard, deployment, docs, responsive docs, and gateway Go tests.

SDK/conformance coverage currently validates 41 executable REST scenarios plus 272 named non-executable coverage scenarios, including executor dispatch, file-spool/NATS worker ingestion, and webhook outbox retry.

After this handoff documentation was added, the following validation passed again:

```powershell
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:docs
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the gateway executor dispatch slice, the following validation passed:

```powershell
cmd /c pnpm check:contracts
cmd /c pnpm test:observability
cmd /c pnpm test:gateway
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

After the worker consumer/result-ingestion slice, the focused validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:worker
cmd /c pnpm test:observability
```

The full post-ingestion validation passed after rerunning `test:v0` and `check` sequentially so the docs responsive server had exclusive port ownership:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm check:contracts
cmd /c pnpm test:conformance
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the Postgres gateway-store slice and review-blocker fix pass, the following validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:gateway
cmd /c pnpm check:contracts
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

Postgres integration tests are env-gated by `UBAG_TEST_POSTGRES_DSN` and skip without a live disposable database.

After the NATS JetStream executor and MinIO artifact-storage review-blocker fix pass, the following validation passed:

```powershell
cmd /c pnpm test:deployment
cmd /c pnpm check:contracts
cmd /c pnpm test:docs
cmd /c pnpm check:docs-responsive
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:sdk
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

NATS and MinIO live integration tests are env-gated by `UBAG_TEST_NATS_URL` and `UBAG_TEST_MINIO_ENDPOINT`.

After the NATS worker-consumer slice, the following validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:worker
cmd /c pnpm test:deployment
cmd /c pnpm test:conformance
cmd /c pnpm check:contracts
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

`cmd /c pnpm test:v0` and `cmd /c pnpm check` were rerun sequentially after an intentional parallel validation attempt caused responsive-check server port contention.

After the signed webhook outbox and retry-worker slice, the following validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:deployment
cmd /c pnpm test:conformance
cmd /c pnpm test:observability
cmd /c pnpm check:contracts
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

Runtime worker ingestion state after this slice:

- Gateway executor mode defaults to `noop`.
- Optional local dispatch is `UBAG_EXECUTOR_MODE=file` with `UBAG_EXECUTOR_SPOOL_DIR` pointing at ignored runtime storage such as `var/executor-spool`.
- Optional durable dispatch is `UBAG_EXECUTOR_MODE=nats`; configure `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`. When `UBAG_WORKER_CONSUMER_ENABLED=true`, the embedded worker consumer leases a durable JetStream pull consumer configured by `UBAG_NATS_WORKER_DURABLE`, `UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`, `UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`. Env-gated NATS integration tests use `UBAG_TEST_NATS_URL`.
- Gateway rejects executable job payloads containing credentials, cookies, tokens, API keys, browser storage/session state, client-supplied noVNC URLs, private keys, MFA/TOTP material, or CAPTCHA-solving instructions before job storage or dispatch.
- File-spool dispatch writes gateway-stamped envelopes under `pending/`; the embedded worker consumer can atomically lease them under `leased/`, invoke the Python worker, ingest gateway-sequenced worker events/results into job history, and finalize under `done/`, `failed/`, or `cancelled/`.
- NATS dispatch publishes gateway-stamped envelopes to `<subject>.<jobID>` and cancellation notices to `<subject>.cancel.<jobID>`. The embedded NATS worker consumer filters `<subject>.*`, reconstructs execution envelopes from persisted jobs before invoking the Python worker, acknowledges only after durable terminal ingestion or synthetic retryable failure, nacks transient setup/store failures with delay, and terminates malformed or mismatched envelopes as poison messages.
- Enable embedded ingestion with `UBAG_WORKER_CONSUMER_ENABLED=true`, `UBAG_WORKER_PYTHON`, `UBAG_WORKER_SCRIPT`, `UBAG_WORKER_POLL_INTERVAL_MS`, and `UBAG_WORKER_MAX_RUNTIME_MS`.
- Gateway stores default to memory. Set `UBAG_GATEWAY_STORE=postgres` and `UBAG_POSTGRES_DSN` to persist gateway jobs, job events, worker-event dedupe keys, and idempotency records in Postgres. `migrations/postgres/0001_gateway_stores.sql` must be applied before `/v1/ready` can pass in Postgres mode. Readiness verifies `gateway_job_id_seq`, `gateway_jobs`, `gateway_job_events`, `gateway_job_worker_event_keys`, and `gateway_idempotency_records`. Set `UBAG_GATEWAY_STORE=sqlite` for a single-node SQLite runtime store (WAL, `busy_timeout`, `foreign_keys`, single-writer guard).
- Artifact storage defaults to memory. Set `UBAG_ARTIFACT_STORE=minio` with `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL` to use MinIO/S3-compatible object storage. Set `UBAG_ARTIFACT_STORE=localfs` with `UBAG_ARTIFACT_DIR` for a local-filesystem artifact store. If Postgres gateway stores are active, apply `migrations/postgres/0002_artifact_metadata.sql`; readiness verifies `artifact_metadata`. Artifact list/get requires `job:read`, upload requires `artifact:write`, delete requires `artifact:delete`, and cross-tenant artifact access is hidden as not found through the owning job lookup. Env-gated MinIO tests use `UBAG_TEST_MINIO_ENDPOINT`, `UBAG_TEST_MINIO_ACCESS_KEY`, and `UBAG_TEST_MINIO_SECRET_KEY`.
- Webhook delivery defaults to an in-memory outbox unless Postgres gateway stores are active. Set `UBAG_WEBHOOK_OUTBOX=postgres`, apply `migrations/postgres/0003_webhook_outbox.sql`, and enable `UBAG_WEBHOOK_WORKER_ENABLED=true` with `UBAG_WEBHOOK_SECRET` or per-secret environment variables for durable signed retries. A SQLite webhook outbox mode is also available for single-node deployments. Callback URLs require `callbacks.webhook_secret_id` when `callbacks.webhook_url` is set and are validated before job storage and before delivery; public hosts must match `UBAG_WEBHOOK_ALLOWED_HOSTS` unless `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true` is explicitly enabled after outbound SSRF review, and unsafe private/local URLs, userinfo, fragments, and secret-looking query keys are rejected unless explicit operator policy allows them. Replay requires an existing tenant/app-scoped delivery ID, idempotency key, and audit reason.
- API-facing job reads return `404` for out-of-scope tenant/app jobs to avoid cross-tenant job existence leaks.

Runtime probe evidence from the latest full run:

- Gateway health URL: `http://127.0.0.1:8080/v1/health`.
- Docs site URL: `http://127.0.0.1:4321/`.
- Dashboard URL: `http://127.0.0.1:4177/`.
- Health status: `ok`.
- API version: `2026-05-22`.
- Readiness status: `ready`.
- Created probe job: `job_000000000001`.
- Event count: `1`.
- SSE contained queued event: `true`.
- Metrics included HTTP request and SSE current gauges: `true`.
- Docs and dashboard returned HTTP 200 with expected titles.

## Resume Procedure

Start every continuation with this exact sequence:

```powershell
git status --short --branch
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

Use `cmd /c pnpm ...` on Windows if PowerShell script policy blocks direct `pnpm` execution.

If you need local manual inspection, use these services:

```powershell
cmd /c pnpm docs:dev
cmd /c pnpm --filter @ubag/dashboard dev --host 127.0.0.1 --port 4177
```

For the gateway edge runtime:

```powershell
$env:UBAG_APP_SECRET="dev-secret"
$env:UBAG_API_VERSION="2026-05-22"
$env:UBAG_GATEWAY_ADDR="127.0.0.1:8080"
make dev-edge
```

## Critical Invariants

- API version is `2026-05-22`.
- App-secret auth binds to the configured tenant/app/role principal; do not trust caller-supplied actor headers as identity.
- Mutating routes require an `Idempotency-Key`; CLI/sidecar may auto-generate one when acting as local clients.
- Artifact PUT/DELETE are mutating routes and require an `Idempotency-Key`; PUT replay returns stored artifact metadata and DELETE replay returns `204`.
- Payloads must not include credentials, cookies, tokens, API keys, secrets, session/browser storage, or CAPTCHA-solving material, including compact key variants such as `apiKeyValue` and `client_secret_value`.
- Gateway-side payload safety checks must run before job storage or executor dispatch.
- Browser automation remains user-owned manual login through live browser/noVNC sessions.
- noVNC URLs must be generated by the runtime and stay loopback/operator-scoped; do not accept arbitrary noVNC URLs from job payloads.
- Safe mode is the default automation stance.
- Standard privacy mode is the current default; HIPAA/GDPR modes require later activation evidence.
- Caddy admin must remain localhost-bound.
- `deploy/small/small.ps1 -Action config` must render from `env.example` unless `-AllowSecretConfigOutput` is explicitly provided.
- Do not expose backing service ports publicly without an explicit firewall and deployment review.

## External Activation Items

These are not missing repository work; they require facts or services outside this checkout.

| Item | Required External Input |
| --- | --- |
| Live AI provider execution | User-owned accounts, manual browser login, active sessions, and provider-specific consent. |
| Small-profile runtime smoke | Docker Desktop Linux engine or an equivalent Docker host. |
| Production deployment | Host, DNS, TLS, secrets, firewall policy, and explicit operator approval. |
| Live webhook endpoint smoke | Real callback targets, outbound allowlist, shared signing secrets, and a disposable network path. |
| HIPAA/GDPR modes | Legal/compliance review, BAAs/DPAs where needed, deployed evidence, and operator policy choices. |
| Marketplace/app distribution | Publishing accounts, release credentials, and governance approval. |

## Next Coding Queue

Pick up from these implementation tracks after preserving the current green baseline:

1. Commit the current green baseline when the user approves.
2. Convert safe-mode provider stubs into live manual-session browser adapters after user-owned account/session requirements are available; acceptance requires manual-session consent, no credential/session/token storage, no CAPTCHA bypass, runtime-generated noVNC URLs only, adapter allowlisting, tenant/app scoping, audit events, and artifact redaction. The orchestration layer (`apps/worker/ubag_worker/orchestration`: topology/AIMD/pacer/channel-pool/bulkhead/scheduler) and cross-engine grid abstractions (`apps/worker/ubag_worker/live/engines.py`, `live/remote.py`) are now implemented and unit-tested as the ToS-safe substrate for this; the live adapter wiring remains the external-account-gated step.
3. DONE (v2.1): Gateway sessions are now minted from the verified SSO principal on `/v1/sso/oidc/callback` and `/v1/sso/saml/acs` (opaque crypto/rand token, SHA-256 at rest, HttpOnly cookie + JSON token, `POST /v1/sso/logout` revokes), bound to tenant/app/role, audited, with no credential/session storage.
4. Replace the pragmatic SAML check with exclusive XML-C14N signature verification before onboarding a production IdP. PARTIAL (v2.1): `internal/sso/canonicalize.go` applies exclusive XML-C14N (`xml-exc-c14n#`) before digest/signature verification and fails closed; harden against a real production IdP before claiming full conformance.
5. DONE (v2.1): Native Postgres stores added for response-cache, workflow, SSO, SCIM, SIEM, and webhook-secret subsystems (migrations `0005_enterprise_stores.sql`, `0006_audit_sessions.sql`); each fails fast via `Ready()`/`to_regclass` so Postgres deployments no longer silently fall back. Round-trip tests are env-gated on `UBAG_TEST_POSTGRES_DSN`.
6. DONE (v2.1): `POST /v1/audit/export` exports real Merkle-chained audit records from `internal/audit` (memory + SQLite + Postgres), accepts the SDK request body (`idempotency_key` ignored read field, optional `range.{from_sequence,to_sequence}` post-filter), and verifies the chain over the full result before windowing.
7. Continue hardening workflow/cache/template runtime beyond the current validated foundation; acceptance requires durable template authoring, richer workflow DAG/saga semantics, retention controls, privacy-mode cache bypasses, and expanded SDK/conformance coverage.
8. Keep SDK release work scoped to TypeScript/JavaScript (`@ubag/sdk`) and Go (`github.com/ubag/ubag-go`); do not reintroduce other SDK package trees without an explicit product decision.
9. Broaden TypeScript/JavaScript and Go SDK conformance beyond REST fixtures where runtime services exist, including event streaming and live binary artifact smoke before new transports are claimed.
10. DONE (v2.1): Worker-side `ConcurrencyRegistry.Report` is wired to AIMD cap-change events. The worker emits `concurrency.cap_changed` telemetry (`orchestration/telemetry.py`); the gateway intercepts it in the `WorkerConsumer` ingest loop and routes it to `topology.ConcurrencyRegistry.Report`, so `/v1/concurrency` reflects live worker-reported lane concurrency. Covered by gateway and worker unit tests.
11. Add CI after remote policy is known. The repository now has a baseline commit (`0364595`, v0 platform) and a v2.1 delta commit (`85d6eb0`); neither is pushed. Postgres round-trip tests can run in CI via `pnpm test:gateway:postgres` (needs `UBAG_TEST_POSTGRES_DSN`; see `docs/postgres-roundtrip-tests.md`).
12. Onboard real live providers using `live_web_template(...)` / `generic_live_web` and `apps/worker/ubag_worker/live/ONBOARDING.md`; activation still requires user-owned provider accounts and manual login.
13. DONE (2026-06-01): Worker runtime orchestration integration (Option A, full). `LiveSessionEngine` accepts an optional `LiveOrchestrator` (`apps/worker/ubag_worker/live/orchestrator.py`) that wires Fleet/ChannelPool/persistent-AIMD/topology into the live path and emits `browser.topology_reported` + `concurrency.cap_changed`; `create_default_driver` now honors `engine_spec_from_env()` via the pure `_resolve_launch_plan` helper. The gateway `WorkerConsumer` projects topology snapshots into its in-memory topology store (tenant-forced, storage-state redacted, nil-safe). Opt-in/backward-compatible. Covered by `apps/worker/tests/test_live_orchestration.py` (21) and gateway `workerconsumer_test.go` topology tests. Live real-browser runs remain externally-blocked (ToS).
14. DONE (2026-06-02): Live-browser viewer (noVNC) admin login stack. Opt-in `live-browser` Compose profile adds `browser-viewer` (`deploy/small/browser-viewer/Dockerfile` + `entrypoint.sh`: Xvfb + fluxbox + Chromium CDP:9222 + x11vnc + websockify/noVNC:6080), published loopback-only as `UBAG_NOVNC_PORT:7900`; CDP stays internal. Caddy `/novnc/*` route (SAMEORIGIN) plus a dashboard **Take control** viewer (lazy sandboxed iframe, `frame-src 'self'`). Worker `_novnc_url` is now `UBAG_NOVNC_BASE_URL`-configurable but loopback-gated (`_is_loopback_novnc_base`), falling back to `http://127.0.0.1:7900`. Gateway passes `UBAG_REMOTE_BROWSER_ENDPOINT`/`UBAG_BROWSER_HEADED`/`UBAG_BROWSER_ENGINE`/`UBAG_BROWSER_PROTOCOL`/`UBAG_NOVNC_BASE_URL`. Documented in `deploy/small/env.example` + `README.md`; asserted by `tools/check-small-deployment.mjs`. Covered by `apps/worker/tests/test_novnc_base_url.py` (7). **Manual human login only — no credential/cookie/storage-state capture, no CAPTCHA/2FA automation. The Docker image was not built/run here (no Linux Docker engine); only static config + worker/dashboard wiring validated.**

## Documentation Update Rule

Whenever implementation changes, update these files in the same slice:

- `PROGRESS.md` for current status, validation evidence, and resume notes.
- `IMPLEMENTATION_COVERAGE.md` for A-Z coverage state.
- `apps/docs/src/content/docs/implementation-coverage.md` for rendered docs coverage.
- This `AGENT_HANDOFF.md` when the resume procedure, validation evidence, runtime state, or remaining coding queue changes.
