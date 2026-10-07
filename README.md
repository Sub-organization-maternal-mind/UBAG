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
- Safe-mode provider adapter manifests and stubs for DeepSeek, ChatGPT, Gemini, Mistral, generic chat, generic form, and mock.
- TypeScript and Go SDKs with shared conformance fixtures for system, jobs, job events/SSE, artifacts, operator collections, webhook replay, workflow/template list, cache, apps/devices/audit, and metrics endpoints.
- CLI package for health/ready/version, jobs, events, apps/devices/audit collections, artifacts, cache, metrics, webhook replay, SSE snapshot reads, and local mock-worker runs.
- NAJM/Hallmark dashboard under `apps/dashboard` with gateway API wiring, strict CSP, self-hosted/system fonts, and accessible state fixtures.
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
cmd /c pnpm dashboard:build
cmd /c pnpm typecheck
cmd /c pnpm lint
cmd /c pnpm check
```

Automated tests were removed on 2026-10-08. The last commit that still has them is tagged `pre-strip-tests`. `pnpm typecheck` and `pnpm lint` are the gates; production issues are fixed by hand.

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

`UBAG_World_Class_Blueprint_v2.1.md`

This version supersedes `UBAG_World_Class_Blueprint_v2.md` (per ADR-0003); the repository-local blueprint is the canonical checked-in reference.
