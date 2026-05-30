---
title: A-Z Implementation Coverage
description: Exact implementation coverage for the UBAG blueprint, with local evidence and external activation requirements.
---

Last updated: 2026-05-29

This ledger maps the UBAG A-Z plan to the current repository implementation. It is intentionally evidence-based: a row is marked **implemented** only when code, docs, configuration, and a validation command exist in this repo.

For future agentic AI continuation, read the root `AGENT_HANDOFF.md` first, then `PROGRESS.md`, then this page. The rendered handoff is also available at `operations/agent-handoff`.

## 2026-05-29 Gateway Runtime + Enterprise Surface Update

The gateway gained a runtime SQLite/localfs persistence path and six enterprise leaf packages. All of this is **code-complete and locally validated** — `apps/gateway` `go build`, `go vet`, and `go test ./...` are green on the Go 1.26 toolchain — with honest follow-ups noted below. The gRPC + grpc-web layer was completed in a previous slice.

New, code-complete & locally validated:

- Runtime stores: `UBAG_GATEWAY_STORE=sqlite` (WAL, `busy_timeout`, `foreign_keys`, single-writer), `UBAG_ARTIFACT_STORE=localfs` with `UBAG_ARTIFACT_DIR`, and a SQLite webhook outbox mode.
- `internal/ratelimit` (sliding-window; memory + SQLite + Postgres stores; policy resolver), `internal/responsecache` (memory + SQLite; never exposes cached payload values via the API), `internal/workflow` (memory + SQLite multi-step runs; payload policy on every step input), `internal/sso` (stdlib OIDC RS256 + SAML verification; memory + SQLite config store), `internal/scim` (SCIM v2 Users/Groups CRUD+Patch; memory + SQLite; passwords never stored), and `internal/siem` (redacted audit/event export via File/HTTP/Syslog sinks with a non-blocking exporter).
- Nil-safe `internal/httpapi` wiring adds `GET /v1/cache` (`job:read`), `DELETE /v1/cache` (`rate_limit:manage`), `GET /v1/rate-limits` (`rate_limit:manage`), `GET/POST /v1/workflows` + `POST /v1/workflows/{id}/runs` + `GET /v1/workflows/runs/{id}` (`job:read`/`job:create`), `GET/PUT /v1/sso/config` (`role:manage`) + `POST /v1/sso/oidc/callback` + `POST /v1/sso/saml/acs`, `/v1/scim/v2/Users[/{id}]` and `/v1/scim/v2/Groups[/{id}]` (`role:manage`), `GET/PUT /v1/siem/config` (`role:manage`) + `POST /v1/audit/export` (`data:export`), `POST /v1/webhooks/secret:rotate` (`secret:rotate`), and a `withRateLimit` middleware that is pass-through when disabled.
- New env vars: `UBAG_RATE_LIMIT_ENABLED` (default false), `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`, `UBAG_SIEM_FILE_PATH`.
- Independent review PASSED with no Critical/High issues; two hardening fixes applied (cache purge returns `501` when disabled; SSO config `PUT` rejects OIDC without an Issuer and SAML without an IdP cert).

Honest limitations / externally-blocked:

- SSO OIDC/SAML callbacks return a verified principal but do not yet mint real gateway sessions (follow-up).
- SAML signature verification is a pragmatic non-full-XML-C14N fails-closed check; adopt exclusive C14N before production IdP onboarding.
- Only the rate limiter has a native Postgres store; cache/workflow/sso/scim/siem/webhook-secrets persist via SQLite or fall back to in-memory.
- `POST /v1/audit/export` returns exporter status/stats only; full record export is a follow-up (audit record source is still a stub).
- Non-TypeScript SDKs (rust/java/ruby/php/csharp/swift/kotlin/elixir) build/test in CI but are not all locally validated (C# 10/10, Swift Windows stdlib broken, cargo/mvn/ruby/php/gradle/mix absent locally).
- Live provider adapters remain externally-blocked.

## Coverage States

| State | Meaning |
| --- | --- |
| Implemented | The repo contains runnable or validateable code/config/docs for the requirement. |
| Contracted | Public contracts, schemas, manifests, docs, and tests exist, but production runtime integration depends on an external system or credential. |
| External activation | The repo contains the implementation path, but execution requires something outside this checkout: provider account login, Docker Desktop Linux engine, deployment host, domain, TLS secret, or marketplace account. |

## Milestone Coverage

| Plan Area | Current State | Evidence |
| --- | --- | --- |
| Docs-first Milestone 0 | Implemented | `apps/docs`, `PRD.md`, `PROGRESS.md`, ADRs, blueprint coverage checker, Starlight build. |
| v0 edge gateway | Implemented | `apps/gateway`, `cmd /c pnpm test:gateway`, `/v1/health`, `/v1/ready`, `/v1/version`, `/v1/metrics`, jobs, tenant/app-scoped `/v1/events`, stream upgrade guard, workflows, built-in template catalog/application, targets/adapters, apps, devices, webhooks, cache status, audit, cancel, retry, paginated collections, event history, SSE snapshot, payload safety gate, executor dispatch boundary, file-spool leasing, NATS durable worker leasing, worker result ingestion, idempotent artifact mutations, and signed webhook outbox delivery. |
| Contracts and schemas | Implemented | `packages/openapi`, `packages/shared-schemas`, `packages/proto`, operation-level REST manifests, `cmd /c pnpm test:schema`, `cmd /c pnpm check:contracts`. |
| Edge queue/storage | Implemented | `packages/edge-store`, SQLite migrations, queue conformance test. The gateway now wires runtime SQLite stores (`UBAG_GATEWAY_STORE=sqlite`) and a localfs artifact store (`UBAG_ARTIFACT_STORE=localfs`, `UBAG_ARTIFACT_DIR`) plus a SQLite webhook outbox mode (code-complete & locally validated); Postgres/MinIO remain the multi-process durable options. |
| Mock worker and adapter | Implemented | `apps/worker`, `adapters/mock`, Python worker tests and smoke output. |
| Built-in provider adapter list | Contracted | Manifests and safe-mode stubs for DeepSeek, ChatGPT, Claude, Gemini, Mistral, Perplexity, generic chat, generic form, and mock. |
| User-owned manual login stance | Contracted | Adapter manifests block network automation until manual session runtime is available; docs prohibit credential scraping and bundled CAPTCHA solving. |
| Dashboard prototype | Implemented | `apps/dashboard`, static NAJM/Hallmark tokenized UI, tabs for Overview, Apps, Targets, Jobs, Sessions, Templates, Runtime, Activation, strict CSP, no external font calls, accessible state fixtures, and responsive check/build scripts. |
| CLI and sidecar path | Implemented | `packages/cli`, `packages/sidecar`, health/ready/version/job/event/artifact/operator/webhook/cache/metrics/SSE/mock-run commands, CLI option parsing regression tests, loopback sidecar health/proxy tests, artifact PUT/DELETE idempotency, factory loopback enforcement, and absolute-form proxy target hardening. |
| SDK wave 1 | Implemented | TypeScript, Python, and Go SDKs validated against shared conformance fixtures for system, jobs, job events, artifacts, operator collections, webhook replay, workflow/template list endpoints, cache status, apps/devices/audit, metrics, and stream entrypoint surfaces. |
| Security and compliance contracts | Implemented | `packages/security`, app-secret, device token, RBAC/ABAC, audit redaction/chaining, webhook signing tests. |
| Observability and ops contracts | Implemented | `packages/observability`, stable metric/event/log/checklist/probe registries, and gateway readiness probe contracts for jobs, idempotency, queue, executor, artifacts, templates, and webhooks. |
| Small deployment profile | Implemented | `docker-compose.small.yml`, `deploy/small`, Caddy/Postgres/Dragonfly/MinIO/Grafana/Prometheus/NATS optional profiles, opt-in Postgres gateway store environment, NATS dispatcher env, MinIO artifact env with least-privilege `minio-init`, webhook worker env, rerunnable Postgres `migrate` action, optional Caddy TLS example, and Postgres migrations `0001`, `0002`, and `0003`. |
| Release/governance/runbook docs | Implemented | Release governance, operator runbook, observability, testing, and compliance docs in `apps/docs`. |
| v1 real provider runtime | Contracted | Provider adapter manifests and safe-mode packages exist. Live browser automation requires user-owned accounts and manual noVNC/browser session runtime activation. |
| v2 enterprise/ecosystem | Contracted | Architecture, deployment, plugin, governance, security, and compliance docs exist; the gateway also ships code-complete & locally validated enterprise leaf packages for rate limiting, response cache, workflow runs, SSO (OIDC/SAML verification), SCIM v2, and SIEM export (see the 2026-05-29 update). Production enterprise integrations still require deployment environment and identity provider activation, and SSO session minting, native Postgres stores for non-rate-limiter subsystems, and a real audit-export source remain follow-ups. |

## Detailed Blueprint Feature Map

| Blueprint Feature Area | Current State | Repo Evidence |
| --- | --- | --- |
| Vision and product surface | Implemented | `PRD.md`, `product/scope`, `product/roadmap`. |
| Engineering principles | Implemented | `product/principles`, ADRs. |
| Open-source stack | Implemented | `architecture/technology-stack`, root package workspace. |
| Deployment profiles | Implemented | `deployment/profiles`, `docker-compose.small.yml`, `deploy/small`. |
| High-level architecture | Implemented | `architecture/overview`, `architecture/repository-structure`. |
| Universal command contract | Implemented | `contracts/job-contract`, shared JSON Schema, SDK fixtures. |
| Job response envelope | Implemented | `packages/shared-schemas/schemas/job-response.schema.json`, SDK tests. |
| Stable error contract | Implemented | `contracts/error-catalog`, `apps/gateway/internal/httpapi/errors.go`. |
| Idempotency semantics | Implemented | `contracts/idempotency`, gateway idempotency service, SDK fixtures, and opt-in Postgres idempotency records for small profile. |
| API versioning | Implemented | `contracts/api-protocols`, `UBAG_DEFAULT_API_VERSION`, OpenAPI headers. |
| Edge/ingress | Implemented | Caddy small profile, gateway health/readiness routes. |
| API gateway | Implemented | `apps/gateway`, OpenAPI `/v1` surface. |
| AuthN/AuthZ | Implemented | App-secret gateway auth plus security package contracts; `internal/sso` adds stdlib OIDC (RS256) and SAML assertion verification with principal mapping via `/v1/sso/*` (code-complete & locally validated; gateway session minting is a follow-up). |
| Tenant registry | Contracted | Tenant headers and security model docs; live tenant DB requires deployment activation. |
| Command validator | Implemented | Gateway create-job validation plus shared schemas. |
| Job orchestrator | Implemented | In-memory v0 job lifecycle, opt-in Postgres job/event store, cancel, retry, scoped job and cross-job events, SSE, internal executor dispatch with no-op default plus optional file spool, atomic file-spool leases, terminal finalization, and worker result ingestion. |
| Prompt template engine | Implemented | Built-in memory-backed template catalog, `/v1/templates`, readiness checks, and create-job template application before payload validation/storage/enqueue; versioned durable template storage, render dry-runs, and A/B tests remain future runtime work. |
| Semantic cache | Contracted | `internal/responsecache` provides a privacy-aware exact-match response cache (memory + SQLite) behind `/v1/cache` that never exposes cached payload values; the semantic/vector backend still requires a deployed cache/vector service. |
| Webhook dispatcher | Implemented | Gateway terminal-job callback projection, signed delivery sender, URL policy validation, memory/Postgres outbox stores, retry/dead-letter worker, replay hardening, observability metrics/events, and `contracts/webhooks`; live delivery still needs operator-owned callback targets and signing secrets. |
| Browser worker fleet | Implemented | Python worker runner, mock adapter, file-spool/NATS consumer bridge, gateway dispatch-envelope compatibility, session docs. |
| Admin dashboard | Contracted | `apps/dashboard` static prototype covers Overview, Apps, Targets, Jobs, Sessions, Templates, Runtime, Activation, and state fixtures; live SvelteKit/API-wired admin dashboard remains v1. |
| Local sidecar | Implemented | `packages/sidecar` loopback runtime, `/health`, `/v1/*` proxy, non-loopback guard, factory loopback enforcement, absolute-form proxy target hardening, and tests. |
| CLI | Implemented | `packages/cli`, health/job/SSE/mock/cancel/retry commands. |
| Plugin system | Contracted | Plugin docs, capability model, governance path. |
| SDK strategy | Implemented | TypeScript, Python, Go SDK wave and conformance fixtures. |
| Integration methods | Implemented | REST, SSE, WebSocket upgrade, CLI, SDKs; gRPC proto contracts. |
| Rate limiting | Implemented | `internal/ratelimit` sliding-window limiter with memory + SQLite + Postgres stores, a policy resolver, `GET /v1/rate-limits`, and a `withRateLimit` middleware that is pass-through when disabled (`UBAG_RATE_LIMIT_ENABLED`, default false); code-complete & locally validated. Live tuning still needs deployment config. |
| Browser sessions | Contracted | Safe manual-login manifests and noVNC/session docs; live sessions require user-owned login. |
| Adapter SDK | Implemented | Adapter manifest contract, mock adapter, registry tests. |
| Built-in adapters | Contracted | Safe-mode manifests/stubs for DeepSeek, ChatGPT, Claude, Gemini, Mistral, Perplexity, generic chat/form, mock. |
| Drift detection | Contracted | Drift docs and adapter manifest hooks. |
| Recording and replay | Implemented | Artifact policy docs, worker output conventions, gateway artifact metadata/download APIs, and idempotent artifact PUT/DELETE replay behavior. |
| Workflow sagas | Implemented | `internal/workflow` multi-step job workflow definitions/runs engine (memory + SQLite) with payload policy enforced on every step input, exposed via `/v1/workflows[/runs]` (code-complete & locally validated); advanced DAG/saga compensation remains future runtime work. |
| Response normalization | Implemented | Gateway worker-event ingestion normalizes mock worker text output into the public job result envelope; additional adapter-specific schemas remain extensible by contract. |
| Caching strategy | Implemented | `internal/responsecache` exact-match response cache (memory + SQLite), `/v1/cache` status/purge with purge returning `501` when disabled, `UBAG_CACHE_ENABLED`/`UBAG_CACHE_TTL_MS`; semantic/vector caching remains future work. |
| Queue Abstraction | Implemented | `packages/edge-store`, SQLite migrations, queue conformance, gateway executor dispatch port, local file-spool dispatch adapter, lease/finalize states, cancellation markers, and NATS JetStream gateway dispatch plus durable worker consumption via `UBAG_EXECUTOR_MODE=nats`. |
| Observability | Implemented | `packages/observability`, `/v1/metrics`, Prometheus scrape config, queue depth/age metrics, worker metric family emission, webhook outbox depth/age metrics, and worker/webhook result metric/event contracts. |
| Performance engineering | Contracted | Acceptance gates and observability metrics. |
| Stability and reliability | Implemented | Runbooks, retries, DLQ docs, queue checks, webhook outbox retries/dead-lettering, monotonic terminal status guards, late-event suppression, and cancelled-job no-reenqueue markers. |
| Database schema | Implemented | SQLite migrations, Postgres gateway/artifact/webhook migrations, schema docs, and env-gated Postgres store tests. |
| Sidecar connector | Implemented | `@ubag/sidecar` package plus conformance coverage scenario. |
| Dashboard IA | Implemented | Dashboard UX docs and rendered dashboard. |
| WASM plugins | Contracted | Plugin system docs and release governance. |
| Multi-region and HA | Contracted | Deployment profile docs; requires enterprise infrastructure. |
| Backup/DR/migration | Contracted | Migration docs and operator runbook. |
| Compliance and privacy | Contracted | Standard mode implemented; HIPAA/GDPR require formal activation evidence. |
| Deployment options | Implemented | Edge and small profiles plus docs. |
| Folder structure | Implemented | Repository structure docs and actual workspace. |
| Development phases | Implemented | Roadmap and progress ledger. |
| Testing strategy | Implemented | Root test scripts, docs gates, conformance checks. |
| Operator runbook | Implemented | Runtime recovery and operator runbook docs. |
| Documentation strategy | Implemented | Docs site, ADRs, coverage checks. |
| Community governance | Implemented | Release/governance docs. |
| Cost and operations | Contracted | Ops docs and metrics; live cost telemetry requires deployed usage data. |
| World-class checklist | Implemented | Blueprint and implementation coverage gates. |

## v2.1 Observability And Concurrency Surfaces

| Capability | Blueprint | State | Repo Evidence |
| --- | --- | --- | --- |
| Browser topology (instance → context → tab) | §12.6–§12.13 | Documented + dashboard + conformance | `worker/multi-tab-orchestration`, dashboard Browser panel, `browser.summary.ok`/`browser.instances.ok`/`browser.contexts.ok`/`browser.tabs.ok`. |
| Adaptive concurrency (AIMD ceilings) | §12.9 | Documented + dashboard + conformance | `worker/multi-tab-orchestration`, dashboard Concurrency panel, `concurrency.list.ok`. |
| Cross-engine and remote grids | §13.10–§13.12 | Documented + conformance coverage | `worker/cross-engine-grids`, `cross_engine` coverage scenario. |
| Manual-action alerts (CAPTCHA/login) | Manual-action policy | Documented + dashboard + conformance | `operations/manual-action-alerts`, dashboard Alerts panel, `alerts.list.ok`/`alerts.config.ok`/`alerts.acknowledge.ok`/`alerts.resolve.ok`. |
| Audit export + Merkle chain | §11.6 | Documented + conformance | `security/audit-export-merkle`, `audit.export.chain-valid`. |
| SSO sessions and logout | SSO/enterprise | Documented + conformance | `security/sso-sessions`, `sso.logout.ok`. |
| Enterprise Postgres persistence | §22 | Documented + conformance coverage | `data/postgres-persistence`, `postgres_persistence` coverage scenario. |

All v2.1 surfaces are presentation-only reads. They never expose credentials, cookies, storage-state URIs (only a boolean `has_storage_state`), or SMTP secrets (only an `smtp_configured` flag).

## External Activation Items

These are not unplanned work items in this checkout; they are the external facts needed to run live environments safely.

| Activation Item | Why It Cannot Be Completed Inside This Local Checkout Alone | Repo Evidence |
| --- | --- | --- |
| Real AI provider sessions | Requires user-owned provider accounts, manual login, and live browser sessions. The repo intentionally does not scrape credentials or solve CAPTCHAs. | `adapters/*/manifest.json`, `worker/safe-user-owned-automation`, `security/browser-login-controls`. |
| Docker small runtime smoke | Requires Docker Desktop Linux engine running on this machine. Compose config is validateable without it. | `docker-compose.small.yml`, `deploy/small/small.ps1`. |
| Live NATS/MinIO/webhook smoke | Requires Docker or equivalent live backing services and an operator-owned callback endpoint to exercise JetStream, MinIO, and signed outbound webhook delivery over the network; repository tests are env-gated by `UBAG_TEST_NATS_URL`, `UBAG_TEST_MINIO_ENDPOINT`, and `UBAG_TEST_POSTGRES_DSN`. | `apps/gateway/internal/executor/nats_test.go`, `apps/gateway/internal/executor/natsconsumer_test.go`, `apps/gateway/internal/artifacts/artifacts_test.go`, `apps/gateway/internal/webhooks`, `deploy/small/README.md`. |
| Production deployment | Requires host, DNS, TLS, secrets, and operator approval. | `deploy/small/env.example`, deployment docs. |
| Marketplace/app distribution | Requires external publishing accounts and release credentials. | Plugin and release governance docs. |
| Formal HIPAA/GDPR certification | Requires legal/compliance review, BAAs/DPAs, data-flow audit, and deployed controls. | Compliance mode docs and security contracts. |

## Local Acceptance Command

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
```

The full `test:v0` chain includes contracts, edge queue/storage with typechecking and SQLite migration execution, security, worker/adapters, TypeScript/Python/Go SDKs, conformance fixtures, observability contracts, CLI, dashboard, deployment config, docs, responsive docs checks, and gateway Go tests. Latest 2026-05-25 continuation validation also passed `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, `git --no-pager diff --check`, and focused gateway, SDK, conformance, schema, deployment, observability, CLI, sidecar, dashboard, contract, and SDK freshness checks.
