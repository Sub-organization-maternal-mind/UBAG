# Orchestration Semantics: Per-Request Model/Mode Selection + Conversation Affinity

Date: 2026-07-15
Status: Approved design (slice 1 of the AI-orchestrator gap plan)

## Background

UBAG already runs the core AI-orchestrator loop in production (`ubag.polytronx.com`): a live Chromium instance with per-provider tabs (ChatGPT, Gemini, DeepSeek), a jobs API with queuing/idempotency/retries, a live Playwright worker that types prompts into provider web UIs and extracts normalized responses, signed webhooks back to calling applications, manual-login-only safe mode, an alerts subsystem, and a mobile monitoring app.

Brainstorming identified five gaps against the full orchestrator vision (per-request model/mode selection, conversation affinity, new provider adapters, automatic fallback, mobile alerting) and grouped them into four sequential slices:

1. **Orchestration semantics (this spec)** — per-request model/mode selection and per-caller conversation affinity.
2. Provider expansion (Kimi, Minimax, Claude activation).
3. Automatic provider fallback/routing.
4. Mobile admin push alerting and alert actions.

Decisions locked during brainstorming: evolve the existing UBAG platform (no rewrite); the caller specifies provider/model/mode per job (automatic routing is deferred to the fallback slice); conversations are identified by a caller-passed opaque key; architecture is a contract-first extension of the existing job pipeline.

## Goals

- A calling application can pin the provider model and thinking mode per job (e.g. Gemini "2.5 Pro" + "extended" thinking) instead of relying on fixed operator defaults.
- A calling application can preserve conversational context across jobs: the same conversation key reaches the same provider chat thread; a new key opens a fresh chat.
- Invalid model/mode requests fail at job creation with stable error codes, before any browser interaction.
- Conversation state is durable, tenant-scoped, observable via the operator API, and safe across worker restarts.
- Existing callers see zero behavior change; every new runtime capability ships inert by default.

## Non-goals

Automatic provider fallback/routing; new provider adapters; mobile push notifications; conversation rotation or summarization; a write API for conversations. Each belongs to a later slice.

## Contract changes (first, per repo rule)

`packages/shared-schemas/schemas/job-request.schema.json`:

- New optional `job.model_settings` object: `{ model?: string, thinking?: string, extras?: object<string,string> }`. Omitted → the adapter's current operator defaults apply.
- The existing `job.conversation_id` field becomes honored as an opaque conversation key scoped to `(tenant, app_id, target)`.
- New `job.options.conversation_missing`: `"fail"` (default) or `"restart"` — what to do when a bound thread no longer exists on the provider side.

`packages/openapi`: mirrors the same fields on job creation and documents `GET /v1/conversations`.

Stable error codes added: `conversation_not_found`, `conversation_broken`, `model_unavailable`, `mode_unavailable`.

Adapter manifests (`adapters/*/manifest.json`) gain a `model_catalog` block declaring supported `models`, `thinking` levels, and `extras` (name → allowed values). Real catalogs for `gemini_web` and `deepseek_web`; an empty catalog for `chatgpt_web` initially (the worker deliberately leaves the ChatGPT account default today); a synthetic catalog for `mock`. `pnpm test:adapter-registry` validates the block. Callers discover catalogs through the existing `/v1/targets` / `/v1/adapters` endpoints.

The gateway validates `model_settings` against the target's catalog at job creation and fails fast with `model_unavailable` / `mode_unavailable`.

Conformance fixtures (`packages/conformance`) and the TypeScript/Go SDKs are updated in the same slice, gated by `check:contracts` and `check:sdk-freshness`.

## Gateway conversation store

New package `apps/gateway/internal/conversations`, modeled on `apps/gateway/internal/alerts` (memory + SQLite + Postgres stores, nil-safe optional wiring):

- Row: `tenant, app_id, target, conversation_key, provider_thread_ref (chat URL/ID), state (active|broken), created_at, last_used_at, last_job_id`.
- Migrations: `migrations/postgres/0008_conversations.sql` and SQLite `0006_conversations.sql` (verify latest numbering at implementation time).
- Dispatch: when a job carries `conversation_id`, the gateway resolves it to a thread ref and injects it into the worker envelope through the existing executor dispatch boundary.
- Ingestion: the worker emits `conversation.thread_bound` / `conversation.thread_broken` / `conversation.thread_rebound` events; the existing `WorkerConsumer` projects them into the store (same pattern as `browser.topology_reported`).
- New read-only route `GET /v1/conversations` (tenant/app-scoped, paginated, `job:read` RBAC, nil-safe 501 when disabled).
- Env flag, inert by default: `UBAG_CONVERSATIONS=off|memory|sqlite|postgres`. When `off`, `conversation_id` is accepted and ignored exactly as today.

## Worker / live engine

- `apps/worker/ubag_worker/live/selectors.py`: `ProviderConfig` gains a `model_catalog` mapping catalog entry names onto the existing choice-setting machinery (menu selectors, `satisfied_when` verification, and the slow-reasoning flag that already lengthens response timeouts). A job's `model_settings` resolves to a per-job enforcement list; absent settings keep today's fixed defaults byte-identical.
- `apps/worker/ubag_worker/live/engine.py` conversation flow:
  - **Resume:** navigate to the thread URL and verify it loaded (URL pattern + provider response container present) before typing. On failure, follow `conversation_missing`: `fail` (default) returns stable `conversation_not_found` and emits `conversation.thread_broken`; `restart` opens a fresh chat, rebinds the key, and emits `conversation.thread_rebound`.
  - **New conversation:** after the first response, capture the canonical chat URL (providers rewrite the URL on first message) and emit `conversation.thread_bound`.
  - **Redaction rule:** thread refs are chat URLs only — never cookies, storage state, or noVNC URLs (same posture as the topology intercept).
- `apps/worker/ubag_worker/orchestration/scheduler.py`: jobs sharing a conversation key run strictly FIFO; distinct conversations remain parallel under the existing AIMD channel caps.
- `apps/worker/ubag_worker/live/events.py`: the three conversation event types.
- The mock adapter honors `model_settings` and conversation binding deterministically so the entire path is CI-testable; live provider paths stay out of CI per existing ToS policy.

## Error handling

- Catalog validation rejects bad requests at creation — nothing invalid reaches a browser.
- Selector drift on a model/mode picker fails the job with screenshot-on-failure artifacts and a drift event, feeding the existing re-baselining workflow.
- All conversation and model failures surface through existing job events → SSE/webhooks → dashboard and mobile app.

## Safe-mode compliance

Unchanged hard constraint: manual login only; no credential/cookie capture; no CAPTCHA handling. Catalog enforcement only clicks visible UI controls inside user-owned sessions.

## Testing

- Contracts: `pnpm lint:schemas`, `pnpm lint:openapi`, `node tools/check-contracts.mjs`, conformance fixtures for the new fields, SDK freshness + TS/Go SDK tests.
- Gateway: Go unit tests for the conversations stores (memory/SQLite; Postgres env-gated), dispatch injection, event ingestion, and `/v1/conversations` pagination/AuthZ/501 behavior.
- Worker: pytest with fake page drivers for catalog resolution, resume/bind/broken/restart flows, and FIFO-per-conversation scheduling.
- End-to-end (CI-safe, mock target): same-key thread reuse, FIFO ordering, `conversation.thread_bound` event, `GET /v1/conversations` listing, and `model_unavailable` rejection at creation.
- Full gates (`pnpm check`, `pnpm test:v0:local`) are run by the operator; live provider verification happens manually in production.

## Rollout

Small, flag-gated commits in this order: contracts → gateway store/validation/routes → worker engine/scheduler/mock adapter → SDKs/CLI/dashboard read view. Default off everywhere; production enablement is an explicit operator action after verification.
