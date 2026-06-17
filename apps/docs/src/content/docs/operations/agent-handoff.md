---
title: Agent Handoff
description: Resume guide for future agentic AI work on UBAG.
---

This page mirrors the root `AGENT_HANDOFF.md` so the docs site contains the same resume context as the repository root.

## Resume Point

Start here before further implementation:

1. Read `AGENT_HANDOFF.md`.
2. Read `PROGRESS.md`.
3. Read `IMPLEMENTATION_COVERAGE.md`.
4. Check the current worktree.

```powershell
git status --short --branch
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:docs
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

## Current State

- Git is initialized on `master`, tracking `origin/master`.
- The current workspace contains intentional TS+Go-only SDK completion edits until reviewed or committed.
- Milestone 0 docs-first baseline is complete.
- Current v0 edge foundation is implemented and validateable.
- Small-profile deployment scaffolding is present and compose-validated.
- Gateway file-spool and NATS worker consumer/result ingestion is implemented for local/dev execution.
- Opt-in Postgres gateway stores are implemented for jobs, events, worker-event dedupe keys, and idempotency records.
- NATS JetStream gateway dispatch and embedded durable worker consumption are implemented with `UBAG_EXECUTOR_MODE=nats`, `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, `UBAG_NATS_SUBJECT`, and the `UBAG_NATS_WORKER_*` settings.
- MinIO artifact storage is implemented with `UBAG_ARTIFACT_STORE=minio`, `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL`, with Postgres metadata in `migrations/postgres/0002_artifact_metadata.sql` when Postgres stores are active.
- Signed webhook outbox delivery is implemented with per-job callbacks, `UBAG_WEBHOOK_OUTBOX`, `UBAG_WEBHOOK_WORKER_ENABLED`, environment-backed signing secrets, strict callback URL policy, replay hardening, and Postgres migration `migrations/postgres/0003_webhook_outbox.sql`.
- Built-in template catalog/runtime foundation is implemented: `/v1/templates` lists built-ins, readiness checks the template store, and job creation applies template defaults before payload validation, storage, idempotency hashing, and executor enqueue.
- Latest hardening covers template-default job creation, callback secret-reference handling, manual-session event data preservation, sidecar artifact idempotency, SDK/CLI endpoint parity, dashboard CSP/state coverage, small-profile public ingress guards, Postgres migration reruns, MinIO least-privilege bootstrap, nginx-dashboard ingress, gateway graceful shutdown, observability readiness/smoke probes, contract drift, and docs claim accuracy.
- Live provider execution and production deployment require external activation inputs.

## Implemented Surface

| Area | Current Evidence |
| --- | --- |
| Documentation | Starlight site, PRD, progress ledger, ADRs, blueprint and implementation coverage. |
| Gateway | Go `/v1` control plane with health, ready, version, metrics, jobs, events, SSE, WebSocket guard, workflows, built-in template catalog/application, targets/adapters, apps, devices, webhooks, cache status, audit, cancel, retry, auth, stable errors, idempotency, executable payload safety checks, executor dispatch, file-spool/NATS leasing, worker result ingestion, signed webhook outbox delivery, and opt-in Postgres jobs/events/idempotency/webhook stores. |
| Contracts | OpenAPI, JSON Schemas, Protobuf seed, SDK fixtures, and contract validation. |
| Worker/adapters | Python worker, mock adapter, safe-mode provider manifests, manual-session events, artifact policies, and secret rejection. |
| SDK/CLI/sidecar | TypeScript and Go SDKs for system, jobs, job events/SSE, artifacts, operator collections, webhook replay, workflow/template list, cache, apps/devices/audit, metrics, and stream entrypoint endpoints; CLI; loopback sidecar; 41 executable REST conformance scenarios plus 272 named coverage scenarios; freshness checks. |
| Dashboard | NAJM/Hallmark operator dashboard with gateway API wiring, Overview, Apps, Targets, Jobs, Sessions, Templates, Runtime, Activation, CSP, no third-party font calls, accessible state fixtures, gateway-native browser topology fields, runtime-provided loopback noVNC embedding only, real template render output, and workflow metadata without fake fixture DAGs. |
| Security/ops | App-secret auth, device tokens, RBAC/ABAC, audit, webhook signing, rate-limit contracts, observability registries, and runbooks. |
| Deployment | Edge profile and small Docker Compose profile with nginx-dashboard ingress, Postgres, Dragonfly, MinIO, Prometheus/Grafana, optional NATS, Postgres gateway/artifact/webhook migrations, rerunnable `migrate` action, least-privilege `minio-init`, and durable-store env wiring. |

## Latest Green Baseline

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

After the 2026-05-25 continuation hardening pass, the following sequential validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:deployment
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git --no-pager diff --check
```

The last full implementation pass passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:deployment
cmd /c pnpm check:contracts
cmd /c pnpm check:sdk-freshness
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

Responsive docs and dashboard verifiers now use OS-assigned local ports and readiness polling to avoid stale local preview or Chrome DevTools port collisions.

After this handoff page and root handoff file were added, the following validation passed again:

```powershell
cmd /c pnpm test:v0
cmd /c pnpm check
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

Dispatch state: gateway executor mode defaults to `noop`. `UBAG_EXECUTOR_MODE=file` plus `UBAG_EXECUTOR_SPOOL_DIR` enables local file-spool dispatch envelopes under ignored runtime storage. `UBAG_EXECUTOR_MODE=nats` publishes accepted jobs and cancellation notices to JetStream. `UBAG_WORKER_CONSUMER_ENABLED=true` enables the embedded local/dev lease loop, which moves file-spool envelopes through `pending -> leased -> done|failed|cancelled` or leases NATS messages through `UBAG_NATS_WORKER_DURABLE`, invokes the configured Python worker using a persisted-job envelope, and ingests gateway-sequenced worker events/results into job history before acknowledging queue work.

Persistent-state state: gateway stores default to memory. `UBAG_GATEWAY_STORE=postgres` plus `UBAG_POSTGRES_DSN` enables Postgres-backed jobs, job events, worker-event dedupe keys, and idempotency records after `migrations/postgres/0001_gateway_stores.sql` is applied. `UBAG_ARTIFACT_STORE=minio` enables MinIO/S3 artifact storage; when Postgres stores are active, apply `migrations/postgres/0002_artifact_metadata.sql` for artifact metadata. `/v1/ready` verifies the required gateway and artifact metadata tables; API-facing job reads return `404` for out-of-scope tenant/app jobs.

Webhook state: job requests can configure terminal callbacks through
`callbacks.webhook_url`, required `callbacks.webhook_secret_id`, and optional
`callbacks.event_types`. Public callback hosts must match
`UBAG_WEBHOOK_ALLOWED_HOSTS` unless `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true` is
explicitly set after outbound SSRF review. `UBAG_WEBHOOK_OUTBOX=postgres` stores
deliveries and attempts in `gateway_webhook_deliveries` and
`gateway_webhook_attempts` after `migrations/postgres/0003_webhook_outbox.sql`;
`UBAG_WEBHOOK_WORKER_ENABLED=true` runs signed retry delivery with bounded
backoff/dead-lettering. Replay requires an existing scoped delivery,
idempotency key, and audit reason.

Runtime probe evidence:

- Gateway: `http://127.0.0.1:8080/v1/health`.
- Docs: `http://127.0.0.1:4321/`.
- Dashboard: `http://127.0.0.1:4177/`.
- Health status `ok`, readiness `ready`, API version `2026-05-22`.
- Probe job `job_000000000001` created; SSE and metrics probes passed.

## Fixed Audit Findings

| Workstream | Closure |
| --- | --- |
| Docs/contracts | Stale docs and contract coverage claims corrected. |
| Gateway/control plane | Auth, payload retention, webhook replay, SSE/WebSocket, metrics, and route tests fixed. |
| Worker/adapters | noVNC URL ownership, secret rejection, manual-session context, safe-mode manifests, and edge fallback fixed. |
| SDK/CLI/sidecar | Freshness generation, command coverage, idempotency behavior, and Python 3.10 compatibility fixed. |
| Security/ops | Rate limits, Caddy admin binding, Grafana placeholder guard, small-profile guardrails, and audit checks fixed. |
| Dashboard/docs site | Stale stack references and responsive gates fixed. |

## Critical Invariants

- API version is `2026-05-22`.
- App-secret auth identity comes from configured bindings, not caller-provided actor headers.
- Mutating routes require idempotency.
- No credentials, cookies, tokens, secrets, or CAPTCHA-solving material in job payloads.
- Browser automation is user-owned manual login through live sessions.
- noVNC URLs must be runtime-generated and loopback/operator-scoped.
- Caddy admin remains localhost-bound.
- Standard privacy mode is default; HIPAA/GDPR require later activation evidence.

## Next Work

Continue from the green baseline with these tracks:

1. Commit the current green baseline when the user approves.
2. Convert safe-mode provider stubs into live manual-session browser adapters after user-owned account/session requirements are available; acceptance requires manual-session consent, no credential/session/token storage, no CAPTCHA bypass, runtime-generated noVNC URLs only, adapter allowlisting, tenant/app scoping, audit events, and artifact redaction.
3. Continue hardening workflow/cache/template runtime beyond the current validated foundation; acceptance requires durable template authoring, richer workflow DAG/saga semantics, retention controls, privacy-mode cache bypasses, and expanded SDK/conformance coverage.
4. Add gRPC/gRPC-Web serving from the Protobuf contracts after stable service boundaries exist; acceptance requires the same authn/authz, idempotency, stable error model, body/message limits, streaming limits, and explicit TLS/origin/CORS policy as REST.
5. Broaden TypeScript and Go SDK conformance beyond REST fixtures where runtime services exist, including event streaming and artifacts before new transports are claimed.
6. Add CI after the repository has an initial commit and remote policy is known.
