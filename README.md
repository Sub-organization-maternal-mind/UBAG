# UBAG

UBAG is the Universal Browser-Automation Gateway: a self-hostable platform that lets applications drive web-based AI and automation targets through stable APIs, SDKs, workers, and operator tooling.

This repository has completed the docs-first Milestone 0 baseline and the current v0 edge foundation slice.

## Current Scope

- Full planning and documentation baseline.
- v0 contracts, OpenAPI, shared schemas, and Protobuf seed contracts.
- Dependency-light Go gateway for health, readiness, version, jobs, scoped cross-job events, idempotency, app-secret auth, paginated operator collections, cancel/retry, SSE snapshot routes, template catalog/application, executor dispatch, worker result ingestion, idempotent artifact mutations, and signed webhook outbox delivery.
- SQLite/localfs-oriented edge store and queue contracts with migrations; the gateway runtime currently uses memory by default and Postgres/MinIO when explicitly configured.
- Security/compliance TypeScript contracts for app-secret auth, device tokens, RBAC/ABAC, audit events, and webhook signing.
- Deterministic Python mock adapter and worker JSONL runner.
- Safe-mode provider adapter manifests and stubs for DeepSeek, ChatGPT, Claude, Gemini, Mistral, Perplexity, generic chat, generic form, and mock.
- TypeScript, Python, and Go SDKs with shared conformance fixtures for system, jobs, job events/SSE, artifacts, operator collections, webhook replay, workflow/template list, cache, apps/devices/audit, and metrics endpoints.
- CLI package for health/ready/version, jobs, events, apps/devices/audit collections, artifacts, cache, metrics, webhook replay, SSE snapshot reads, and local mock-worker runs.
- Static NAJM/Hallmark dashboard prototype under `apps/dashboard` with local mock data, strict CSP, self-hosted/system fonts, and accessible state fixtures.
- Small Docker Compose profile under `deploy/small` and `docker-compose.small.yml`.
- Observability/QA package for stable metrics, events, logs, smoke checklist, and health probes.
- Astro Starlight docs site under `apps/docs`.
- PRD and progress ledger at the repository root.
- Architecture, contracts, worker, adapters, data, security, dashboard, operations, testing, release, and ADR documentation.

## Commands

```powershell
cmd /c pnpm install
cmd /c pnpm docs:dev
cmd /c pnpm docs:build
cmd /c pnpm check:docs-responsive
cmd /c pnpm test:schema
cmd /c pnpm test:edge-store
cmd /c pnpm test:security
cmd /c pnpm test:worker
cmd /c pnpm test:sdk
cmd /c pnpm test:conformance
cmd /c pnpm test:observability
cmd /c pnpm test:cli
cmd /c pnpm test:dashboard
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:gateway
cmd /c pnpm test:v0
cmd /c pnpm check
```

`test:gateway` and Go SDK checks use `go` from `PATH` when available, otherwise the repo test runner uses the portable Go toolchain under `%LOCALAPPDATA%\CodexToolchains`.

Postgres gateway-store and webhook outbox integration tests are optional and skipped by default.
Use a disposable database because the tests apply
`migrations/postgres/0001_gateway_stores.sql` and dependent migrations before
exercising Postgres-backed job/event, idempotency, artifact metadata, and
webhook outbox stores:

```powershell
$env:UBAG_TEST_POSTGRES_DSN="postgres://ubag:password@127.0.0.1:5432/ubag_test?sslmode=disable"
cmd /c pnpm test:gateway
Remove-Item Env:\UBAG_TEST_POSTGRES_DSN
```

The edge gateway can be started with:

```powershell
$env:UBAG_APP_SECRET="dev-secret"
make dev-edge
```

The small Docker Compose profile is scaffolded at `docker-compose.small.yml` with
configuration and run docs under `deploy/small`:

```powershell
Copy-Item deploy\small\env.example deploy\small\env.local
notepad deploy\small\env.local
.\deploy\small\small.ps1 -Action config
.\deploy\small\small.ps1 -Action up
```

`-Action config` renders with `deploy\small\env.example` by default to avoid printing local secrets; pass `-AllowSecretConfigOutput` only when you intentionally need to inspect rendered `env.local` values.

See `IMPLEMENTATION_COVERAGE.md` and the docs page `implementation-coverage` for the exact A-Z coverage ledger and external activation items.

## Agent Continuation

Future agentic AI work should start with `AGENT_HANDOFF.md`, then `PROGRESS.md`, then `IMPLEMENTATION_COVERAGE.md`. These files document the current green baseline, validation evidence, local URLs, fixed audit findings, external activation items, and next coding queue.

## Source Blueprint

The implementation plan is based on:

`UBAG_World_Class_Blueprint_v2.md`

Audit attachments may provide an external copy, but the repository-local blueprint is the canonical checked-in reference.
