# UBAG — Universal Browser-Automation Gateway

Self-hostable platform that lets applications drive web-based AI and automation targets through stable APIs, SDKs, workers, and operator tooling. Docs-first repo: `PRD.md`, `UBAG_World_Class_Blueprint_v2.1.md`, and `PROGRESS.md` (live verification ledger) govern scope.

## Agent operating rules (read first — every session, every coding agent)

- **Skip long builds/CI during routine coding.** Don't run full suites (`pnpm test:v0:local`, `pnpm check`, full gateway builds, etc.) as part of normal implementation work — do small, targeted checks only (a single test, `go vet`, a quick lint). Code the bulk of the change, then commit and push once it's done; the user runs full verification separately and will report any errors back.
- **Never act on assumptions.** When a decision needs the user's input (ambiguous scope, missing config/credentials, a choice between approaches), stop and ask in a clarifying question that presents your top recommendation(s) as selectable options — don't guess and implement.

## Tech stack

- pnpm workspace monorepo (Node 25+, TypeScript) — `apps/` + `packages/`
- Go: `apps/gateway` (dependency-light HTTP gateway), `packages/sdk-go`, `deploy/operator`
- Python: `apps/worker` (JSONL runner + provider adapters), `tests/chaos`
- Rust: `packages/sidecar-rust`; `apps/mobile` is Tauri 2 + Svelte (read-only gateway monitoring)
- Docs: Astro Starlight (`apps/docs`); dashboard: `apps/dashboard` (NAJM/Hallmark theme, strict CSP)

## Commands

- Install: `pnpm install`
- Full local gate: `pnpm test:v0:local` — add Go gateway tests with `pnpm test:v0`
- Aggregate check: `pnpm check` (blueprint coverage + contracts + SDK freshness + key suites)
- Per-area suites: `pnpm test:schema | test:edge-store | test:security | test:worker | test:sdk | test:conformance | test:observability | test:cli | test:dashboard | test:deployment | test:docs | test:gateway`
- Contract lint: `pnpm lint:openapi`, `pnpm lint:schemas`, `pnpm lint:proto`
- Gateway (Go) via Makefile: `make gateway-build | gateway-run | gateway-test | gateway-vet`
- Dev servers: `pnpm docs:dev`, `pnpm dashboard:dev`
- Go tests resolve `go` from PATH, else the portable toolchain under `%LOCALAPPDATA%\CodexToolchains` (`tools/run-go-tests.mjs`)
- Postgres gateway-store / webhook-outbox integration tests are optional and skipped by default (`pnpm test:gateway:postgres`)
- Windows quirk: README invokes pnpm as `cmd /c pnpm ...` — use that form if plain `pnpm` misbehaves in your shell

## Architecture orientation

- `apps/gateway` (Go): health/readiness/version, jobs, scoped cross-job events (SSE), idempotency, app-secret auth, artifacts, template catalog, executor dispatch, worker result ingestion, signed webhook outbox. Runtime uses the in-memory store by default; Postgres/MinIO only when explicitly configured.
- `apps/worker` (Python): v0 worker; JSONL runner drives provider adapters. A warm-browser daemon + stdin/stdout protocol exists behind flags (inert by default — see conventions).
- `adapters/`: safe-mode manifests + stubs per provider (chatgpt_web, claude_web, gemini_web, deepseek_web, mistral_lechat, perplexity_web, generic_chat, generic_form, mock) with `registry.json` as the index.
- `packages/`: contracts first — `openapi/`, `shared-schemas/`, `proto/` define the API; `sdk-typescript/` + `sdk-go/` are validated against shared `conformance/` fixtures; plus `security/` (auth, RBAC/ABAC, audit, webhook signing contracts), `edge-store/` (SQLite/localfs store + queue contracts; `migrations/` at repo root), `observability/`, `cli/`, `sidecar/` + `sidecar-rust/`, `adapter-registry/`, `plugins/`.
- `tools/`: the `check-*.mjs` / `run-*.mjs` scripts behind the pnpm `test:*` and `check:*` commands.
- `deploy/` + `docker-compose.small.yml`: small self-host profile; `deploy/operator` is Go.

## Conventions & rules

- **Safe-mode is a hard product constraint** (`adapters/registry.json`): user-owned sessions only; automated login, credential scraping, credential storage, and CAPTCHA solving are forbidden. Never add adapter behavior that violates this.
- **Contracts first**: change `packages/openapi` / `shared-schemas` / `proto` before implementations; `pnpm check:blueprint` and `pnpm check:contracts` gate coverage.
- **PROGRESS.md is the live verification ledger** — read it (and `AGENT_HANDOFF.md`) before implementing; update it whenever scope, commands, runtime status, or remaining work changes.
- Risky runtime features land behind env flags, inert by default (e.g. `UBAG_WORKER_DAEMON`), layered in small commits.
- UI work: `design.md` locks the dashboard to the NAJM theme DNA (warm cream paper, terracotta accent, editorial rhythm). Read `design.md` before UI edits; the project-scoped Hallmark skill lives at `.codex/skills/hallmark/SKILL.md`. Don't invent metrics, testimonials, or logos.

## Gotchas

- The repo previously lived at `D:\Projects\UBAG` on a different machine; `AGENTS.md` and older docs contain stale absolute paths (`D:\...`, `C:\Users\Dr Faisal Maqsood PC\...`). The current root is `E:\Projects\UBAG` — translate stale paths, never propagate them.
- Web-adapter DOM selectors are brittle and get re-baselined against the live pages (see gemini_web `prompt_input` history) — verify against the live DOM before changing selector baselines.

## Code navigation

Prefer semantic tools over grepping or reading whole files: Serena's symbol tools (`find_symbol`, `find_referencing_symbols`, `get_symbols_overview`, `search_for_pattern`) for navigation/edits, and CodeGraph queries for dependency/impact questions. Fall back to Grep/Read only when the semantic tools can't answer.
