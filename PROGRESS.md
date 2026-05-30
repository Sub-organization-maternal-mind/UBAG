# UBAG Progress Ledger

Last updated: 2026-05-30

## Current Phase

v0 edge platform baseline: contracts, gateway, gateway executor dispatch boundary with file-spool and NATS worker result ingestion, opt-in Postgres gateway stores, NATS JetStream executor, MinIO artifact storage with idempotent mutations, signed webhook outbox delivery, built-in template catalog/application, scoped cross-job events, paginated operator collections, hardened payload secret-key detection, edge queue/store contracts, worker/adapters, static dashboard prototype, CLI, SDK wave 1, security/compliance contracts, observability contracts, and small-profile deployment scaffolding.

## Agent Continuation Handoff

Future agentic AI work must start with `AGENT_HANDOFF.md`, then this ledger, then `IMPLEMENTATION_COVERAGE.md`.

The handoff now records:

- Current worktree and Git state.
- Latest green validation commands.
- Runtime probe evidence and local URLs.
- Fixed subagent audit findings.
- Critical implementation invariants.
- External activation items.
- Exact next coding queue.

Rendered docs-site counterpart: `operations/agent-handoff`.

## Status Summary

| Area | Status | Evidence / Next Step |
| --- | --- | --- |
| Agent handoff | Complete | Root `AGENT_HANDOFF.md` plus docs page `operations/agent-handoff` document the resume point for future agents. |
| Git baseline | Complete | Repository initialized; existing project files preserved. |
| pnpm workspace | Complete | Root workspace, lockfile, and `apps/docs` package created. |
| Astro Starlight docs site | Complete | `cmd /c pnpm docs:build` passed and built the current docs site. |
| PRD | Complete | Root `PRD.md` defines goals, milestone boundaries, phases, risks, and current implementation posture. |
| Progress ledger | Complete | This file maps blueprint features to milestones. |
| ADRs | Complete | Seven ADR pages document locked decisions. |
| Blueprint coverage | Complete | `cmd /c pnpm check:blueprint` passed with 69 required docs. |
| Public contracts | Complete | OpenAPI, shared JSON Schemas, Protobuf contract checks, executable conformance fixtures, and coverage scenarios added; `cmd /c pnpm test:schema` validates OpenAPI, JSON Schemas, and proto contract parity. |
| Gateway control plane | Complete | `cmd /c pnpm test:gateway` passes through the portable Go-aware test runner, including `/v1` routes, scoped cross-job event history, collection pagination/AuthZ, SSE/WebSocket, metrics, AuthZ boundaries, webhook replay, validation, idempotency, artifact mutation idempotency, cancel, and retry. |
| Gateway executor dispatch and ingestion | Complete | Gateway create/retry now rejects unsafe executable payloads before storage, dispatches accepted jobs once through an internal executor port, supports no-op, local file-spool, and NATS modes, leases worker envelopes from file-spool or JetStream, ingests normalized worker events/results, and exposes queue/worker/result-ingestion metric families. |
| Human-in-the-loop manual-action alerts | Complete | New `apps/gateway/internal/alerts` package (memory/sqlite/postgres stores, log/SMTP/multi sinks, dedupe + lifecycle). Worker `session.manual_action_required` events raise alerts and email a human (default recipient `mindreader420123@gmail.com`) to solve CAPTCHA/login/verification in the live browser session. `/v1/alerts`, `/v1/alerts/config`, `/v1/alerts/{id}/acknowledge`, `/v1/alerts/{id}/resolve` (operator+ RBAC, nil-safe 501). Postgres migration `0007_alerts.sql`, SQLite `0005_alerts.sql`. `node tools/run-go-tests.mjs apps/gateway` and `node tools/check-contracts.mjs` green. |
| Template catalog runtime | Complete | `/v1/templates` returns built-in scoped templates, readiness verifies the template store, and job creation applies template defaults before payload policy validation, storage, idempotency hashing, and executor enqueue. |
| Postgres gateway stores | Complete | `UBAG_GATEWAY_STORE=postgres` enables Postgres-backed gateway jobs, events, worker-event dedupe keys, and idempotency records after `migrations/postgres/0001_gateway_stores.sql` is applied; default remains memory. Readiness verifies all required gateway SQL objects, API job reads hide cross-tenant job existence, and env-gated integration tests are documented. |
| NATS JetStream executor | Complete | `UBAG_EXECUTOR_MODE=nats` dispatches jobs via JetStream and can consume them through the embedded durable worker queue when `UBAG_WORKER_CONSUMER_ENABLED=true`; stream/subject/durable/ack/nak/max-delivery settings are configurable; env-gated integration tests skip without `UBAG_TEST_NATS_URL`. |
| MinIO artifact storage | Complete | `UBAG_ARTIFACT_STORE=minio` persists job artifacts to MinIO/S3; metadata backend is Postgres (when `UBAG_GATEWAY_STORE=postgres`) or in-memory; REST API at `/v1/jobs/{id}/artifacts[/{key}]`; artifact PUT/DELETE require idempotency and replay safely; migration `0002_artifact_metadata.sql` added. |
| Signed webhook outbox | Complete | Per-job terminal callbacks enqueue signed deliveries, strict URL validation blocks unsafe callbacks, `UBAG_WEBHOOK_OUTBOX=postgres` persists deliveries/attempts after migration `0003_webhook_outbox.sql`, `UBAG_WEBHOOK_WORKER_ENABLED=true` runs bounded retry/dead-letter delivery, and replay now requires an existing scoped delivery. |
| Edge queue/store (SQLite/localfs runtime) | Code-complete & locally validated | `cmd /c pnpm test:edge-store` runs queue conformance and SQLite migration checks. The gateway now wires runtime SQLite stores via `UBAG_GATEWAY_STORE=sqlite` (WAL, `busy_timeout`, `foreign_keys`, single-writer), a localfs artifact store via `UBAG_ARTIFACT_STORE=localfs`/`UBAG_ARTIFACT_DIR`, and a SQLite webhook outbox mode. All Go `build`/`vet`/`test ./...` pass; live multi-process durability still benefits from Postgres/MinIO. |
| Rate limiter (`internal/ratelimit`) | Code-complete & locally validated | Sliding-window limiter with memory + SQLite + Postgres stores and a policy resolver; `withRateLimit` middleware is pass-through when disabled (`UBAG_RATE_LIMIT_ENABLED`, default false). Go package tests pass. `GET /v1/rate-limits` (`rate_limit:manage`). |
| Response cache (`internal/responsecache`) | Code-complete & locally validated | Privacy-aware cache (memory + SQLite) that never returns cached payload values via the API; `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`. `GET /v1/cache` (`job:read`), `DELETE /v1/cache` (`rate_limit:manage`) returns 501 when the cache is disabled. Go package tests pass. |
| Workflow engine (`internal/workflow`) | Code-complete & locally validated | Multi-step job workflow definitions/runs (memory + SQLite) with payload policy enforced on every step input. `GET/POST /v1/workflows`, `POST /v1/workflows/{id}/runs`, `GET /v1/workflows/runs/{id}` (`job:read` / `job:create`). Go package tests pass. |
| SSO (`internal/sso`) | Code-complete & locally validated | Stdlib-only OIDC (RS256) and SAML assertion verification, principal mapping, and config store (memory + SQLite). `GET/PUT /v1/sso/config` (`role:manage`), `POST /v1/sso/oidc/callback`, `POST /v1/sso/saml/acs` (verification, no RBAC). Callbacks now mint a server-side session (opaque crypto/rand token, SHA-256-hashed at rest, HttpOnly+Secure+SameSite=Lax cookie + JSON `session_token`); `POST /v1/sso/logout` revokes. SAML now applies exclusive XML-C14N (`internal/sso/canonicalize.go`) before digest/signature verification and fails closed. Go package tests pass. |
| SCIM v2 (`internal/scim`) | Code-complete & locally validated | SCIM v2 Users/Groups CRUD + Patch store (memory + SQLite); passwords are never stored. `/v1/scim/v2/Users[/{id}]`, `/v1/scim/v2/Groups[/{id}]` (`role:manage`). Go package tests pass. |
| SIEM export (`internal/siem`) | Code-complete & locally validated | Audit/event export with redaction and File/HTTP/Syslog sinks via a non-blocking exporter with graceful shutdown; `UBAG_SIEM_FILE_PATH`. `GET/PUT /v1/siem/config` (`role:manage`), `POST /v1/audit/export` (`data:export`) now streams the real persisted, Merkle-chained audit records (`internal/audit`, memory + SQLite + Postgres) for the requesting tenant with `chain_valid` + `head_hash` plus exporter stats. The handler accepts the SDK request body shape (`idempotency_key` is accepted and ignored for this read; optional `range.{from_sequence,to_sequence}` applies a post-query sequence window with chain verification computed over the full chain before filtering), reconciling the SDKs with the gateway's `DisallowUnknownFields` decoder. Go package tests pass (incl. `TestAuditExportAcceptsSDKBodyAndSequenceWindow`). |
| Webhook secret rotation | Code-complete & locally validated | `POST /v1/webhooks/secret:rotate` (`secret:rotate`) performs reference-based secret rotation with no plaintext stored. Go package tests pass. |
| Security contracts | Complete | `cmd /c pnpm test:security` tests and validates app-secret, device token, RBAC/ABAC, rate-limit, audit, and webhook signing contracts. |
| Mock worker/adapter | Complete | `cmd /c pnpm test:worker` runs Python adapter and worker tests plus compileall and smoke output. |
| Provider adapter registry | Complete | Safe-mode manifests exist for all listed v1 AI providers and generic adapters; worker dispatch enforces ownership/consent context and emits manual-session events. |
| SDK wave 1 | Complete | `cmd /c pnpm test:sdk` validates generated operation-level contract manifest freshness plus TypeScript, Python, and Go SDKs against shared fixtures for system, job, event, artifact, operator collection, webhook replay, workflow/template, cache, apps/devices/audit, metrics, and stream entrypoint surfaces. |
| Sidecar | Complete | `cmd /c pnpm test:sidecar` validates the loopback `@ubag/sidecar` health/proxy runtime, mutating-route idempotency generation including artifact PUT/DELETE, public-binding guard, factory loopback enforcement, and absolute-form proxy target hardening. |
| CLI | Complete | `cmd /c pnpm test:cli` builds/typechecks and tests health/ready/version/create/get/list/cancel/retry/SSE plus list-events/list-targets/list-adapters/list-apps/list-devices/list-audit-events/list-webhooks/list-artifacts/get-artifact/put-artifact/delete-artifact/replay-webhook/cache-status/metrics, help, diagnostics surface, adapter-test command, and mock-worker smoke. |
| Dashboard prototype | Complete | `cmd /c pnpm test:dashboard` checks and builds the static NAJM/Hallmark dashboard prototype with CSP, no third-party font calls, responsive gates, and accessible state fixtures. |
| Small deployment profile | Complete | `cmd /c pnpm test:deployment` validates Compose config for core and optional profiles, including Postgres migration runner, MinIO least-privilege bootstrap, and optional Caddy TLS ingress. |
| Observability contracts | Complete | `cmd /c pnpm test:observability` validates metrics, events, logs, smoke checklist, and health probes. |
| v0 test chain | Complete | `cmd /c pnpm test:v0` passes end-to-end, including gateway Go tests. |
| Plugin & adapter-registry checks | Complete | Root `test:plugins` (20/20) and `test:adapter-registry` (16/16) pass and are wired into `test:v0:local`. |

## 2026-05-29 Gateway Runtime Stores + Enterprise Surface Pass

This pass added a SQLite/localfs runtime persistence path and six new enterprise leaf packages to the Go gateway. All work is in `apps/gateway` on the Go 1.26 toolchain; `go build`, `go vet`, and `go test ./...` are green. The gRPC + grpc-web layer was completed in a previous slice.

Completed and locally validated:

- SQLite gateway store mode (`UBAG_GATEWAY_STORE=sqlite`) with WAL, `busy_timeout`, `foreign_keys`, and a single-writer guard; localfs artifact store (`UBAG_ARTIFACT_STORE=localfs`, `UBAG_ARTIFACT_DIR`); and a SQLite webhook outbox mode.
- `internal/ratelimit` — sliding-window rate limiter with memory + SQLite + Postgres stores and a policy resolver.
- `internal/responsecache` — privacy-aware response cache (memory + SQLite); cached payload values are never exposed via the API.
- `internal/workflow` — multi-step job workflow definitions/runs engine (memory + SQLite) with payload policy enforced on every step input.
- `internal/sso` — stdlib-only OIDC (RS256) and SAML assertion verification, principal mapping, and config store (memory + SQLite).
- `internal/scim` — SCIM v2 Users/Groups CRUD + Patch store (memory + SQLite); passwords are never stored.
- `internal/siem` — audit/event export with redaction and File/HTTP/Syslog sinks via a non-blocking exporter with graceful shutdown.
- HTTP wiring in `internal/httpapi` is nil-safe/optional, so existing behavior is unchanged when the new subsystems are unconfigured. New routes and RBAC actions:
  - `GET /v1/cache` (`job:read`), `DELETE /v1/cache` (`rate_limit:manage`).
  - `GET /v1/rate-limits` (`rate_limit:manage`).
  - `GET/POST /v1/workflows`, `POST /v1/workflows/{id}/runs`, `GET /v1/workflows/runs/{id}` (`job:read` / `job:create`).
  - `GET/PUT /v1/sso/config` (`role:manage`), `POST /v1/sso/oidc/callback`, `POST /v1/sso/saml/acs` (verification, no RBAC).
  - `/v1/scim/v2/Users[/{id}]`, `/v1/scim/v2/Groups[/{id}]` (`role:manage`).
  - `GET/PUT /v1/siem/config` (`role:manage`), `POST /v1/audit/export` (`data:export`).
  - `POST /v1/webhooks/secret:rotate` (`secret:rotate`) — reference-based rotation, no plaintext stored.
  - `withRateLimit` middleware (pass-through when disabled).
- New environment variables: `UBAG_RATE_LIMIT_ENABLED` (default false), `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`, `UBAG_SIEM_FILE_PATH`. Existing store selectors now accept `UBAG_GATEWAY_STORE=memory|postgres|sqlite` and `UBAG_ARTIFACT_STORE=memory|localfs|minio` with `UBAG_ARTIFACT_DIR`.
- Root `package.json` added `test:plugins` and `test:adapter-registry` and wired them into `test:v0:local`; both pass (plugins 20/20, adapter-registry 16/16).
- Independent review PASSED with no Critical/High issues. Two hardening fixes were applied: cache purge now returns `501` when the cache is disabled, and SSO config `PUT` now rejects OIDC without an Issuer and SAML without an IdP certificate.

Honest limitations / externally-blocked items (not yet done in this checkout):

- SSO OIDC/SAML callbacks return a verified principal but do NOT yet mint real gateway sessions (follow-up).
- SAML signature verification uses a pragmatic (non-full XML-C14N) check that fails closed; adopt exclusive C14N before production IdP onboarding.
- For Postgres deployments, only the rate limiter has a native Postgres store; cache, workflow, SSO, SCIM, SIEM, and webhook-secret state persist via SQLite or fall back to in-memory (documented follow-up).
- `POST /v1/audit/export` currently returns exporter status/stats; full record export is a follow-up because the audit record source is still a stub.
- Non-TypeScript SDKs (rust/java/ruby/php/csharp/swift/kotlin/elixir) build and test in CI with their own toolchains and are not all locally validated (cargo/mvn/ruby/php/gradle/mix are absent on this dev machine; C# validated 10/10; the Swift Windows stdlib is broken).
- Live provider adapters still require real accounts/sessions and remain externally-blocked.

Gateway validation (Go 1.26 toolchain):

```powershell
go build ./...
go vet ./...
go test ./...
cmd /c pnpm test:plugins
cmd /c pnpm test:adapter-registry
cmd /c pnpm test:v0:local
```

## 2026-05-30 v2.1 Observability Presentation Surfaces

This pass added ToS-safe, presentation-only observability for the v2.1 multi-tab/concurrency surfaces across the dashboard, docs, and conformance fixtures. No gateway runtime behavior changed; all new dashboard data has a mock fallback and a live `/v1` overlay.

Completed and locally validated:

- **Dashboard panels (`apps/dashboard`).** Three new read-only tabs: **Browser** (instance → provider context → channel tab topology with `warming|ready|busy|draining|quarantined` state badges and a boolean storage-state indicator — never a URI), **Concurrency** (per provider/identity AIMD cap, min/max bounds, in-flight, last-change reason), and **Alerts** (human-in-the-loop CAPTCHA/manual-login queue with Acknowledge/Resolve actions plus an SMTP status line that shows `smtp_configured` yes/no, never a password). Mock data in `mock-data.js`, live `/v1/browser/*`, `/v1/concurrency`, `/v1/alerts[/config]` clients in `gateway-client.js`, render + delegated alert-action handler in `app.js`, redaction guards in `scripts/check.mjs`, responsive `.alert-actions` CSS.
- **Docs (`apps/docs`).** Six new Starlight pages wired into the sidebar: `worker/multi-tab-orchestration` (§12.6–§12.13), `worker/cross-engine-grids` (§13.10–§13.12), `operations/manual-action-alerts`, `security/audit-export-merkle` (§11.6), `security/sso-sessions`, `data/postgres-persistence` (§22). Coverage marked in `blueprint-coverage.md` and `implementation-coverage.md`.
- **Conformance (`packages/conformance`).** 11 new executable replay scenarios (`browser.summary.ok`, `browser.instances.ok`, `browser.contexts.ok`, `browser.tabs.ok`, `concurrency.list.ok`, `alerts.list.ok`, `alerts.config.ok`, `alerts.acknowledge.ok`, `alerts.resolve.ok`, `audit.export.chain-valid`, `sso.logout.ok`) and 7 new named coverage scenarios (categories `multi_tab_topology`, `adaptive_concurrency`, `manual_action_alerts`, `audit_export_chain`, `sso_session`, `cross_engine`, `postgres_persistence`) — now 41 executable + 19 coverage. `validate-fixtures.mjs` gained redaction guards: no `"storage_state_uri"`, `alerts.config.ok` has no password and exposes `smtp_configured`, browser context/tab rows carry a boolean `has_storage_state`.

Redaction honored end-to-end: storage state is a boolean indicator only (never a URI) in the dashboard and fixtures, and alert config exposes an `smtp_configured` flag only (never the SMTP password).

Validation (Windows, `cmd /c pnpm`) passed:

```powershell
node apps/dashboard/scripts/check.mjs                     # Dashboard check passed
node apps/dashboard/scripts/build.mjs                     # dist written
node apps/dashboard/scripts/verify-responsive.mjs         # all 11 tabs pass at 320/375/414/768
node packages/conformance/scripts/validate-fixtures.mjs   # Validated 41 scenarios
node tools/check-contracts.mjs                            # Contract checks passed
node tools/check-blueprint-coverage.mjs                   # 69 required docs present
cmd /c pnpm --filter @ubag/docs build                     # 73 pages built, Complete!
```

## 2026-05-30 Enterprise SSO/Audit Follow-up Closure

This pass closed the three v2.0 enterprise follow-ups left open in the prior gateway slice. All work is ToS-safe, security-hardening only, and lives in `apps/gateway` on the Go 1.26 toolchain.

Completed and locally validated:

- **Audit record persistence + full export.** New `internal/audit` package: a Merkle-chained, append-only, per-tenant audit log with memory + SQLite + Postgres backends (mirrors the webhooks store conventions, including `Ready()`). Each record stores `prev_hash` and its own `record_hash` (SHA-256 over canonical `tenant|app|actor|action|resource|outcome|timestamp|attributes|prev_hash`). Records are emitted (nil-safe) on every `authorizeGatewayAction` allow/deny decision and on SSO session mint/logout. `POST /v1/audit/export` (`data:export`) now streams the real persisted records for the requesting tenant (optional `since`/`until`/`limit` filter), verifies the chain, and returns `chain_valid`, `head_hash`, `count`, `records`, plus exporter `stats`.
- **SSO session minting.** New `internal/session` package: opaque session tokens (`crypto/rand`, 32 bytes, base64url) with only the SHA-256 hash persisted (memory + SQLite + Postgres), the mapped `Principal`, `issued_at`/`expires_at` (TTL default 1h via `UBAG_SESSION_TTL_MS`), and a soft `revoked` flag. OIDC/SAML callbacks now mint a session, set a `HttpOnly`+`Secure`+`SameSite=Lax`+`Path=/` cookie (`ubag_session`), and return `session_token`/`session_expires_at` in JSON. `withAuth` now resolves a session principal from the cookie or bearer token in addition to the existing static `UBAG_APP_SECRET` path (sessions are additive; expired/revoked tokens are rejected). New `POST /v1/sso/logout` revokes the presented session, clears the cookie, and is idempotent.
- **Exclusive XML-C14N for SAML.** `internal/sso/canonicalize.go` applies exclusive canonicalization (`http://www.w3.org/2001/10/xml-exc-c14n#`) to `SignedInfo` and the signed assertion subtree before digest/signature verification, with no new dependencies. Verification fails closed on any mismatch. Documented limitations: single prefix per namespace URI, no `InclusiveNamespaces` PrefixList, no DTD-defaulted attributes, and the subtree must carry its own namespace declarations.

New migration: `migrations/postgres/0006_audit_sessions.sql` (`gateway_audit_log` with `UNIQUE(tenant_id, seq)` + tenant/occurred-at indexes, and `gateway_sessions` keyed by `token_hash` + expiry index; registers the `0006` row). SQLite parity is provided by each store's `Ready()` self-create. New env var: `UBAG_SESSION_TTL_MS` (default 1h).

Security measures: `crypto/rand` token generation, SHA-256 hashing of session tokens at rest, parameterized SQL throughout, per-tenant advisory locking (Postgres) / single-writer transaction (SQLite) to keep the audit chain intact under concurrency, fail-closed C14N + signature verification, and no secrets logged. Sessions and audit default to in-memory stores in `NewServer`, and all emit paths are nil-safe, so existing behavior is unchanged when unconfigured.

Deferred to the contracts/SDK agents (out of scope for this pass): OpenAPI/SDK updates for the new `session_token`/cookie response fields, the enriched `/v1/audit/export` body, and the `/v1/sso/logout` route.

Validation (Go 1.26 local toolchain) passed:

```powershell
node tools/run-go-tests.mjs apps/gateway   # build + all gateway packages green
node tools/check-contracts.mjs             # Contract checks passed
```

## 2026-05-25 Continuation Hardening Pass

This pass closed concrete repo-local gaps reported by the 10 parallel continuation auditors:

- Job creation now applies built-in template defaults before target, command, and input validation, enabling template-only creates while preserving mismatch rejection.
- Payload safety allows secret-reference identifiers such as `webhook_secret_id` while continuing to reject plaintext secret, token, credential, cookie, MFA, TOTP, CAPTCHA, bearer, and private-key material.
- Worker/gateway event ingestion preserves runtime-generated loopback `novnc_url` and safe `session_id` only for `session.manual_action_required`; unsafe or non-loopback values remain redacted.
- Sidecar idempotency now covers artifact PUT/DELETE and emits 26-character ULID-style keys.
- SDK/CLI surfaces were expanded for apps, devices, audit, metrics, readiness/version, artifact get/put, cache status, and stream entrypoints.
- Observability smoke and readiness checks now use the portable small-profile health probe and require template readiness evidence.
- Dashboard security/state coverage now removes Google Fonts, adds strict CSP, sends matching local-preview security headers, and renders reachable loading, empty, partial, error, permission-denied, and stale/offline fixtures.
- Small-profile Caddy ingress blocks unauthenticated public `/v1/metrics*` and `/v1/ready*` while keeping private-network Prometheus/gateway probes available.
- Small-profile deployment hardening now includes an explicit rerunnable Postgres `migrate` action, a `minio-init` least-privilege artifact user/policy bootstrap, separate MinIO root and gateway credentials, and an optional `Caddyfile.tls.example` path for public-domain automatic HTTPS.
- Gateway startup now handles SIGINT/SIGTERM with graceful HTTP shutdown.
- OpenAPI/conformance/proto drift was reduced with the `standard` cache profile enum, fixture-required readiness/version/job-list fields, and a protobuf error envelope.
- Docs now distinguish implemented v0 runtime surfaces from contracted SQLite/localfs, full dashboard, additional SDKs, production auth/rate-limit/audit, and external activation work.

Focused and full validation passed:

```powershell
cmd /c pnpm test:conformance
cmd /c pnpm test:sdk
cmd /c pnpm test:cli
cmd /c pnpm test:sidecar
cmd /c pnpm test:dashboard
cmd /c pnpm test:observability
cmd /c pnpm test:schema
cmd /c pnpm test:deployment
cmd /c pnpm test:gateway
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git --no-pager diff --check
```

## 2026-05-24 Hardening Pass

The latest post-sweep pass closed concrete repo-local gaps found by parallel auditors and validation reruns:

- Sidecar server creation now rejects accidental public bindings by default, builds gateway proxy targets from local paths only, and strips problematic hop-by-hop response headers.
- Bearer auth scheme parsing is case-insensitive while preserving token validation.
- SDK contract freshness includes `job-response.schema.json` so all generated manifests reflect response-envelope schema changes.
- Contract and edge-store checks now require and execute webhook outbox migration coverage; edge-store typechecking is part of the root test surface.
- Gateway readiness probes require queue, executor, artifacts, and webhooks checks in addition to jobs/idempotency.
- CLI value options now reject a following option token as a missing value, with regression coverage and README command-surface updates.
- Small-profile gateway image uses the Go version required by `go.mod` and prepares the executor spool directory for the non-root runtime.
- Dashboard and docs responsive verifiers use OS-assigned local ports, isolated Chrome DevTools ports, and page-readiness polling to avoid stale server/port collisions.
- Final audit closure made app-secret comparison length-safe, disabled environment proxy use for webhook delivery clients, preserved webhook URL policy on fallback clients, and globally ordered in-memory cross-job events by creation time then event ID.
- OpenAPI, JSON Schema, SDKs, and conformance now agree on numeric job-event cursor aliasing, required nullable `next_cursor`, webhook replay response shape, callback secret requirements, operation-level REST manifests, and artifact delete coverage.
- Go SDK artifact upload/download helpers now match the TypeScript/Python artifact surfaces.
- Small-profile `config` renders from `env.example` by default unless `-AllowSecretConfigOutput` is explicit, Caddy no longer enables unscribed admin metrics, and observability smoke probes invoke the small-profile PowerShell helper through a portable Node wrapper.

Focused and full validation passed:

```powershell
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:sidecar
cmd /c pnpm test:security
cmd /c pnpm test:edge-store
cmd /c pnpm test:observability
cmd /c pnpm test:cli
cmd /c pnpm test:deployment
cmd /c pnpm test:dashboard
cmd /c pnpm test:docs
cmd /c pnpm check:contracts
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
cmd /c pnpm check:docs-responsive
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

## 2026-05-24 Gateway Completion Sweep

The latest completion pass closed the remaining repo-local audit blockers from the 10-subagent sweep:

- Payload policy now rejects secret-like key variants such as `token`, `password_value`, `apiKeyValue`, `client_secret_value`, `cookie_header`, and session-token fields while preserving documented `manual_session` and `session_id`.
- `/v1/events` now returns real tenant/app-scoped job events with cursor/limit pagination instead of a placeholder response.
- Operator collection routes apply route-specific authorization and cursor/limit pagination.
- Artifact PUT and DELETE require `Idempotency-Key`; PUT replays return the stored artifact metadata with `idempotent_replay`, and DELETE replays remain `204`.
- Adapter catalog parity now exposes Mistral under `mistral_lechat`.
- Proto, OpenAPI, schema, deployment, and responsive-check command surfaces are provider-neutral and Windows-safe.
- SDK contract manifests were regenerated after OpenAPI/proto contract changes.

Focused validation already passed:

```powershell
cmd /c pnpm test:schema
cmd /c pnpm check:contracts
cmd /c pnpm lint:proto
cmd /c pnpm check:sdk-freshness
cmd /c pnpm --filter @ubag/conformance validate
cmd /c pnpm test:worker
cmd /c pnpm test:dashboard
cmd /c pnpm test:deployment
cmd /c pnpm test:gateway
```

## Latest Completion Snapshot

This snapshot captures the state future agents should preserve before further coding:

- Milestone 0 docs-first baseline is complete.
- Current v0 edge foundation is implemented and validateable.
- Gateway-side executor dispatch is implemented with default no-op mode, optional local file-spool mode, optional NATS JetStream mode, pre-storage payload safety checks, atomic file leases, durable NATS leases, and embedded worker event/result ingestion.
- Opt-in Postgres gateway stores are implemented for jobs, job events, worker-event dedupe keys, and idempotency records.
- NATS JetStream executor mode is implemented (`executor/nats.go`); lazy connection, JetStream stream/consumer, job envelopes published with Nats-Msg-Id deduplication.
- MinIO artifact store is implemented (`artifacts/minio.go`, `artifacts/memory.go`, `artifacts/store.go`, `artifacts/postgres_meta.go`); REST artifact sub-routes added to httpapi server; artifact_metadata Postgres migration added.
- Signed webhook outbox is implemented (`internal/webhooks`); terminal job callbacks enqueue HMAC-signed deliveries, delivery attempts retry/dead-letter through an opt-in worker, Postgres migration `0003_webhook_outbox.sql` adds durable storage, and replay is scoped to existing deliveries.
- Built-in template catalog and create-job template application are implemented; unknown templates and target/command mismatches fail before job storage or enqueue, and SDK conformance now covers read-only workflow/template/cache endpoints.

## Runtime Probe Snapshot

An earlier local runtime probe passed with gateway, docs, and dashboard serving locally. The current NATS/MinIO integration is covered by unit, contract, deployment, and env-gated integration tests; live backing-service smoke still requires Docker or equivalent services.

Latest local service URLs from the green runtime probe:

| Surface | URL | Evidence |
| --- | --- | --- |
| Gateway | `http://127.0.0.1:8080/v1/health` | Health status `ok`, readiness `ready`, metrics available. |
| Docs | `http://127.0.0.1:4321/` | Returned HTTP 200 with expected title. |
| Dashboard | `http://127.0.0.1:4177/` | Returned HTTP 200 with expected title. |

Runtime probe details:

- Created job: `job_000000000001`.
- Event count: `1`.
- SSE stream contained the queued event.
- Metrics contained HTTP request and SSE current gauges.

## Fixed Parallel Audit Findings (v0 Baseline)

| Workstream | Fixed Findings |
| --- | --- |
| Docs/contracts | Stale docs and contract coverage statements were corrected. |
| Gateway/control plane | Auth spoofing risk, unsafe executable payload handling, webhook replay idempotency, SSE/WebSocket behavior, metrics coverage, and route tests were fixed. |
| Worker/adapters | noVNC URL ownership, mock secret rejection, manual-session context enforcement, safe-mode manifests, and edge fallback behavior were fixed. |
| SDK/CLI/sidecar | Generated contract freshness, CLI command coverage, mutating-route idempotency behavior, and Python 3.10 compatibility were fixed. |
| Security/ops | Rate-limit contracts, Caddy admin binding, Grafana placeholder guard, small-profile public binding guardrails, and audit checks were fixed. |
| Dashboard/UX/docs site | Stale stack references and responsive gates were fixed. |

## Later Parallel Review Findings

| Slice | Subagent Count | Key Fixes |
| --- | --- | --- |
| Postgres gateway-store | 8 | Readiness verification, cross-tenant isolation, adapter registry, env documentation, deployment handoff. |
| NATS/MinIO integration | 10 | Artifact auth/limits, metadata readiness, NATS dedupe, migration coverage, SDK regeneration. |
| NATS worker-consumer | 10 | Queue abstraction, envelope reconstruction, ack/nak semantics, poison message handling. |
| Signed webhook outbox | 10 | URL validation before storage, retry/dead-letter, redaction, replay hardening, observability. |
| Template/catalog runtime | 10 | Built-in template catalog, create-job template application, readiness coverage, schema/SDK conformance expansion, file-spool retry fix, webhook DNS policy confirmation. |
| Completion sweep | 10 | File-spool retry, artifact upload/download hardening, Compose healthchecks, Caddy admin/metrics alignment, observability probes, and documentation freshness. |

## v0 Foundation Slice

Implemented scope:

- Public REST contract, shared schemas, Protobuf seed, and SDK conformance fixtures.
- Gateway control plane with app-secret bearer auth bound to configured tenant/app/role principal, route validation, stable errors, tenant/app authorization boundaries, idempotency, runtime metrics, jobs with validated executable payload handling and pre-storage safety rejection, durable event history, live SSE tailing, WebSocket upgrade with validated nonce and heartbeat frames, workflows, built-in template catalog/application, targets/adapters, apps, devices, webhooks, idempotent webhook replay, cache status, audit, cancel, and retry.
- Gateway executor dispatch boundary with gateway-stamped job envelopes, default no-op executor, optional local file-spool dispatcher/consumer, atomic `pending -> leased -> done|failed|cancelled` spool lifecycle, gateway-owned worker event/result ingestion, queue readiness/metrics, and recursive rejection of credentials, cookies, tokens, API keys, browser storage/session state, client-supplied noVNC URLs, private keys, MFA/TOTP material, and CAPTCHA-solving instructions before storage or enqueue.
- Postgres small-profile gateway stores for accepted jobs, event history, worker-event deduplication, and mutating-route idempotency records, selected by `UBAG_GATEWAY_STORE=postgres` with memory as the default.
- Edge queue/store TypeScript contracts plus SQLite migration files and conformance checks.
- Security/compliance TypeScript contracts with tests and validation script, including rate-limit decisions.
- Deterministic mock adapter, Python worker JSONL runner, safe-mode provider adapters, artifact policy validation, secret-material rejection, and manual-session required events.
- TypeScript, Python, and Go SDKs for jobs, system endpoints, workflow/template list endpoints, cache status, apps/devices/audit, metrics, artifact get/put/delete, and SSE helpers with generated contract-manifest freshness checks.
- CLI, loopback sidecar with idempotency auto-generation for mutating proxy routes, static dashboard prototype, observability package, and small-profile deployment scaffolding.
- Root command surface for full v0 verification.

Command surface:

```powershell
cmd /c pnpm test:schema
cmd /c pnpm test:edge-store
cmd /c pnpm test:security
cmd /c pnpm test:worker
cmd /c pnpm test:sidecar
cmd /c pnpm test:sdk
cmd /c pnpm test:conformance
cmd /c pnpm test:observability
cmd /c pnpm test:cli
cmd /c pnpm test:dashboard
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:gateway
cmd /c pnpm test:v0
```

Expected current state:

- `test:schema` validates canonical contract files, schemas, migrations, conformance fixtures, and documented schema anchors.
- `test:edge-store` validates queue semantics and SQLite migrations.
- `test:security` validates app-secret, device token, RBAC/ABAC, rate-limit decisions, audit redaction/chaining, and webhook signing contracts.
- `test:worker` validates Python mock adapter, worker behavior, safe provider manifests, artifact policies, secret rejection, and manual-session context enforcement.
- `test:sidecar` validates loopback health, gateway proxying, idempotency auto-generation, and non-loopback binding rejection.
- `test:sdk` validates generated contract freshness plus TypeScript, Python, and Go SDKs.
- `test:conformance` validates shared SDK conformance fixtures.
- `test:observability` validates metrics, event names, log shape, health probes, and smoke checklist contracts.
- `test:cli` validates CLI typecheck/build/help/create/get/list/cancel/retry/SSE/mock-run plus the diagnostics and adapter-test command surface.
- `test:dashboard` validates dashboard checks and build.
- `test:deployment` validates small-profile Compose config plus NATS/MinIO deployment env guardrails.
- `test:docs` runs the docs build plus responsive docs gate.
- `test:gateway` runs Go gateway tests using `go` from `PATH` or the local portable Codex toolchain, including public route surface checks.
- Postgres integration tests are env-gated by `UBAG_TEST_POSTGRES_DSN`; NATS and MinIO integration tests are env-gated by `UBAG_TEST_NATS_URL` and `UBAG_TEST_MINIO_ENDPOINT`. The default suite compiles and skips them without live backing services.
- `test:v0` chains all v0 checks and passes locally.

## Blueprint Feature Coverage

| Blueprint Feature Area | Milestone | Documentation Page |
| --- | --- | --- |
| Vision and product surface | M0 | `product/scope` |
| Engineering principles | M0 | `product/principles` |
| Open-source stack | M0 | `architecture/technology-stack` |
| Deployment profiles | M0, v0-v2 | `deployment/profiles` |
| High-level architecture | M0 | `architecture/overview` |
| Universal command contract | M0, v0 | `contracts/job-contract` |
| Job response envelope | M0, v0 | `contracts/job-contract` |
| Stable error contract | M0, v0 | `contracts/error-catalog` |
| Idempotency semantics | M0, v0 | `contracts/idempotency` |
| API versioning | M0, v0 | `contracts/api-protocols` |
| Edge/ingress | M0, v0 | `deployment/profiles` |
| API gateway | M0, v0 | `architecture/control-plane` |
| AuthN/AuthZ | M0, v0-v1 | `security/model` |
| Tenant registry | M0, v0 | `security/model` |
| Command validator | M0, v0 | `architecture/control-plane` |
| Job orchestrator | M0, v0 | `contracts/job-lifecycle` |
| Prompt template engine | M0, v0-v1 | `product/roadmap` |
| Semantic cache | M0, v1 | `data/storage` |
| Webhook dispatcher | M0, v0-v1 | `contracts/webhooks` |
| Browser worker fleet | M0, v0-v1 | `worker/architecture` |
| Admin dashboard | M0, v0-v1 | `dashboard/ux` |
| Local sidecar | M0, v0-v1 | `sdk-cli-sidecar` |
| CLI | M0, v0-v1 | `sdk-cli-sidecar` |
| Plugin system | M0, v2 | `plugins` |
| SDK strategy | M0, v0-v2 | `sdk-cli-sidecar` |
| Integration methods | M0, v0-v1 | `contracts/api-protocols` |
| Rate limiting | M0, v0-v1 | `security/model` |
| Browser sessions | M0, v0-v1 | `worker/sessions` |
| Adapter SDK | M0, v0 | `adapters/contract` |
| Built-in adapters | M0, v1 | `adapters/provider-rollout` |
| Drift detection | M0, v1 | `adapters/drift-detection` |
| Recording and replay | M0, v1 | `worker/artifacts` |
| Workflow sagas | M0, v1 | `contracts/job-lifecycle` |
| Response normalization | M0, v1 | `contracts/job-contract` |
| Caching strategy | M0, v1 | `data/storage` |
| Queue Abstraction | M0, v0-v1 | `data/queue` |
| Observability | M0, v1 | `operations/observability` |
| Performance engineering | M0, v1 | `testing/acceptance-gates` |
| Stability and reliability | M0, v1 | `operations/runbook` |
| Database schema | M0, v0-v1 | `data/schema` |
| Sidecar connector | M0, v0-v1 | `sdk-cli-sidecar` |
| Dashboard IA | M0, v0-v1 | `dashboard/ux` |
| WASM plugins | M0, v2 | `plugins` |
| Multi-region and HA | M0, v2 | `deployment/profiles` |
| Backup/DR/migration | M0, v1-v2 | `deployment/migrations` |
| Compliance and privacy | M0, v1-v2 | `compliance/modes` |
| Deployment options | M0, v0-v2 | `deployment/profiles` |
| Folder structure | M0 | `architecture/repository-structure` |
| Development phases | M0 | `product/roadmap` |
| Testing strategy | M0, v0-v2 | `testing/strategy` |
| Operator runbook | M0, v1 | `operations/runbook` |
| Documentation strategy | M0 | `documentation-system` |
| Community governance | M0, v2 | `release/governance` |
| Cost and operations | M0, v1 | `operations/runbook` |
| World-class checklist | M0, v0-v2 | `blueprint-coverage` |
| A-Z implementation coverage | v0-v2 | `implementation-coverage` |

## Verification Checklist

- [x] `cmd /c pnpm install`
- [x] `cmd /c pnpm install --frozen-lockfile`
- [x] `cmd /c pnpm check:blueprint`
- [x] `cmd /c pnpm docs:build`
- [x] `cmd /c pnpm test:schema`
- [x] `cmd /c pnpm test:edge-store`
- [x] `cmd /c pnpm test:security`
- [x] `cmd /c pnpm test:worker`
- [x] `cmd /c pnpm test:sidecar`
- [x] `cmd /c pnpm test:sdk`
- [x] `cmd /c pnpm check:sdk-freshness`
- [x] `cmd /c pnpm test:conformance`
- [x] `cmd /c pnpm test:observability`
- [x] `cmd /c pnpm test:cli`
- [x] `cmd /c pnpm test:dashboard`
- [x] `cmd /c pnpm test:deployment`
- [x] `cmd /c pnpm test:docs`
- [x] `cmd /c pnpm test:v0:local`
- [x] `cmd /c pnpm test:gateway`
- [x] `cmd /c pnpm test:v0`
- [x] `cmd /c pnpm check`
- [x] Docs site opens locally at `http://127.0.0.1:4321/`.
- [x] Hallmark responsive gates checked at 320, 375, 414, 768, and desktop through `cmd /c pnpm check:docs-responsive`.

## Verification Evidence

- Blueprint coverage: 69 required docs present.
- Astro/Starlight build: current static docs site builds successfully.
- Type checks: `astro check` reported 0 errors, 0 warnings, 0 hints.
- Schema contract check: `cmd /c pnpm test:schema` passed; it validates canonical schemas, OpenAPI route coverage, Protobuf parity, conformance fixtures, migrations, and docs anchors.
- OpenAPI validation: `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml` passed.
- JSON Schema validation: `cmd /c pnpm --package=ajv-cli --package=ajv-formats dlx ajv compile -s "packages/shared-schemas/schemas/*.json" --spec=draft2020 -c ajv-formats` passed.
- Edge queue/store: `cmd /c pnpm test:edge-store` passed; 9 queue conformance checks and SQLite migration execution passed.
- Security contracts: `cmd /c pnpm test:security` passed; 7 Node tests plus the contract validation script passed.
- Mock worker/adapter: `cmd /c pnpm test:worker` passed; Python unittests, compileall, safe-mode manifest checks, manual-session event checks, gateway dispatch-envelope compatibility, and a 16-event JSONL smoke run passed.
- SDK freshness: `cmd /c pnpm check:sdk-freshness` passed for TypeScript, Python, and Go generated contract manifests.
- Sidecar: `cmd /c pnpm test:sidecar` passed; typecheck/build plus loopback health, `/v1/*` proxy with idempotency auto-generation, and non-loopback guard tests passed.
- SDK: `cmd /c pnpm test:sdk` passed; TypeScript typecheck/build, Python unittest conformance, and Go conformance tests completed.
- Conformance fixtures: `cmd /c pnpm test:conformance` passed; 30 executable REST scenarios plus 12 named non-executable coverage scenarios validated, including executor dispatch, file-spool/NATS worker ingestion, and webhook outbox retry.
- Observability contracts: `cmd /c pnpm test:observability` passed; metric/event/log/probe/smoke registries validated.
- CLI: `cmd /c pnpm test:cli` passed; CLI typecheck/build/help/create/get/list/apps/devices/audit/events/artifacts/cache/metrics/webhook replay/cancel/retry/SSE/mock-run completed.
- Dashboard: `cmd /c pnpm test:dashboard` passed; dashboard check and build completed.
- Deployment profile: `cmd /c pnpm test:deployment` passed; Docker Compose config validates for core and optional profiles.
- Docs gate: `cmd /c pnpm test:docs` passed; responsive check asserted the UBAG title/H1 and no horizontal overflow at 320, 375, 414, 768, and 1440 px.
- v0 local chain: `cmd /c pnpm test:v0:local` passed end-to-end.
- Gateway: `cmd /c pnpm test:gateway` passed using Go 1.26.3 from `%LOCALAPPDATA%\CodexToolchains`, including the declared `/v1` route surface, event history, SSE/WebSocket, validation, tenant/app authorization, webhook replay, and `/v1/metrics`.
- v0 full chain: `cmd /c pnpm test:v0` passed end-to-end.
- Diff hygiene: `git diff --check` passed.
- Agent handoff docs: root `AGENT_HANDOFF.md` and docs page `operations/agent-handoff` added so future agents can resume without rediscovery.
- Hardening pass: sidecar loopback/proxy safety, length-safe bearer auth comparison, SDK freshness inputs with operation-level REST manifests, webhook migration/proxy/allowlist checks, edge-store typechecking, readiness probes, CLI option parsing, small-profile config safety, gateway image/runtime setup, and responsive verifier isolation were fixed.
- Final audit validation: `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git --no-pager diff --check` passed sequentially.
- 2026-05-25 continuation validation: focused `cmd /c pnpm test:conformance`, `cmd /c pnpm test:sdk`, `cmd /c pnpm test:cli`, `cmd /c pnpm test:sidecar`, `cmd /c pnpm test:dashboard`, `cmd /c pnpm test:observability`, `cmd /c pnpm test:schema`, `cmd /c pnpm test:deployment`, and `cmd /c pnpm test:gateway` passed; full `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git --no-pager diff --check` passed sequentially.
- Full post-hardening validation: `cmd /c pnpm check:sdk-freshness`, `cmd /c pnpm test:sidecar`, `cmd /c pnpm test:security`, `cmd /c pnpm test:edge-store`, `cmd /c pnpm test:observability`, `cmd /c pnpm test:cli`, `cmd /c pnpm test:deployment`, `cmd /c pnpm test:dashboard`, `cmd /c pnpm test:docs`, `cmd /c pnpm check:contracts`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, `cmd /c pnpm check:docs-responsive`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, and `git diff --check` passed.
- Post-handoff validation: after adding the handoff docs, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, and `git diff --check` passed again.
- Gateway executor dispatch slice: `cmd /c pnpm check:contracts`, `cmd /c pnpm test:observability`, and `cmd /c pnpm test:gateway` passed after adding the payload safety gate, executor dispatch port, no-op/file-spool dispatchers, queue/worker metrics, and docs coverage updates.
- Full post-dispatch validation: `cmd /c pnpm test:v0`, `cmd /c pnpm check`, and `git diff --check` passed after regenerating SDK contract manifests.
- Final docs/API validation: `cmd /c pnpm test:docs`, `cmd /c pnpm check:contracts`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git diff --check` passed.
- Worker consumer/result ingestion slice: `cmd /c pnpm test:gateway`, `cmd /c pnpm test:worker`, and `cmd /c pnpm test:observability` passed after adding file-spool leasing/finalization, the embedded worker consumer, Python runner compatibility, result ingestion normalization, cancellation guards, and worker result-ingestion metrics.
- Full post-ingestion validation: `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm check:contracts`, `cmd /c pnpm test:conformance`, `cmd /c pnpm test:deployment`, `cmd /c pnpm test:docs`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git diff --check` passed. `test:v0` and `check` were rerun sequentially to avoid concurrent docs responsive server port contention.
- Postgres gateway-store slice: eight parallel review agents inspected gateway stores, idempotency, migrations, deployment, docs ledgers, security, QA, and scope boundaries. Blocking findings were fixed by strengthening Postgres readiness to require all gateway SQL objects, hiding cross-tenant job existence as 404, copying the full adapter registry into worker-capable small-profile images, documenting `UBAG_TEST_POSTGRES_DSN`, and correcting stale deployment/profile handoff docs.
- Full post-Postgres-store validation: `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm test:gateway`, `cmd /c pnpm check:contracts`, `cmd /c pnpm test:deployment`, `cmd /c pnpm test:docs`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git diff --check` passed. Postgres integration tests remain env-gated by `UBAG_TEST_POSTGRES_DSN` and skip without a live disposable database.
- NATS/MinIO integration slice: ten parallel subagents reviewed HTTP artifact routes, main wiring, artifact packages, NATS executor behavior, deployment config, docs, test gaps, migrations, artifact security, and validation. Blocking findings were fixed by adding artifact route authorization and upload limits, MinIO/Postgres metadata readiness, object-key versioning, NATS cancel dedupe/stream setup, migration ledger coverage, deployment guards, OpenAPI routes, SDK manifest regeneration, and stale-doc cleanup.
- Full post-NATS/MinIO validation: `cmd /c pnpm test:deployment`, `cmd /c pnpm check:contracts`, `cmd /c pnpm test:docs`, `cmd /c pnpm check:docs-responsive`, `cmd /c pnpm check:sdk-freshness`, `cmd /c pnpm test:sdk`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, and `git diff --check` passed. NATS and MinIO live integration tests remain env-gated by `UBAG_TEST_NATS_URL` and `UBAG_TEST_MINIO_ENDPOINT`.
- NATS worker-consumer slice: ten parallel subagents reviewed gateway NATS dispatch, worker protocol, tests, deployment, docs, security, observability, contracts, and implementation approach. The gateway worker consumer now uses a shared worker queue/lease abstraction, consumes NATS JetStream jobs through a durable pull consumer, filters out cancel subjects, reconstructs execution envelopes from persisted jobs, acks only after terminal ingestion or synthetic retryable failure, nacks transient setup/store failures with delay, and terminates malformed or mismatched envelopes as poison messages.
- Full post-NATS-worker validation: `cmd /c pnpm test:gateway`, `cmd /c pnpm test:worker`, `cmd /c pnpm test:deployment`, `cmd /c pnpm test:conformance`, `cmd /c pnpm check:contracts`, `cmd /c pnpm check:sdk-freshness`, `cmd /c pnpm test:docs`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git diff --check` passed. `test:v0` and `check` were rerun sequentially after an intentional parallel attempt caused responsive-check server port contention.
- Webhook outbox slice: ten parallel subagents reviewed architecture, gateway/storage/security/tests/deploy/docs/observability/contracts/implementation risks. The gateway now validates job callback URLs before storage, projects terminal jobs into a signed webhook outbox, supports memory and Postgres outbox stores, retries delivery with bounded backoff/dead-lettering, exposes outbox readiness and metrics, redacts callback metadata, and rejects fabricated webhook replay IDs.
- Full post-webhook validation: `cmd /c pnpm test:gateway`, `cmd /c pnpm test:deployment`, `cmd /c pnpm test:conformance`, `cmd /c pnpm test:observability`, `cmd /c pnpm check:contracts`, `cmd /c pnpm test:docs`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml`, and `git diff --check` passed.
- Responsive screenshots:
  - `.codex/test-output/docs-responsive/ubag-docs-home-320.png`
  - `.codex/test-output/docs-responsive/ubag-docs-home-375.png`
  - `.codex/test-output/docs-responsive/ubag-docs-home-414.png`
  - `.codex/test-output/docs-responsive/ubag-docs-home-768.png`
  - `.codex/test-output/docs-responsive/ubag-docs-home-1440.png`
  - `apps/dashboard/.codex/test-output/dashboard-responsive/ubag-dashboard-320.png`
  - `apps/dashboard/.codex/test-output/dashboard-responsive/ubag-dashboard-375.png`
  - `apps/dashboard/.codex/test-output/dashboard-responsive/ubag-dashboard-414.png`
  - `apps/dashboard/.codex/test-output/dashboard-responsive/ubag-dashboard-768.png`
  - `apps/dashboard/.codex/test-output/dashboard-responsive/ubag-dashboard-1440.png`

## External Activation Items

These are external execution requirements, not untracked repo work:

- Live AI provider execution requires user-owned provider accounts and completed manual login in a live browser/noVNC session.
- Small-profile runtime smoke requires Docker Desktop's Linux engine to be running.
- Production deployment requires host, DNS, TLS, operator secrets, and deployment approval.
- Formal HIPAA/GDPR certification requires legal/compliance review and deployed environment evidence.

## Handoff Rule

Continue in small reviewable slices, but do not put secrets, provider credentials, CAPTCHA solving, or credential scraping into the repository.

Before any future implementation slice, run:

```powershell
git status --short --branch
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

Next coding queue is documented in `AGENT_HANDOFF.md`. Update this ledger and the handoff file whenever implementation scope, validation evidence, runtime status, or remaining work changes.
