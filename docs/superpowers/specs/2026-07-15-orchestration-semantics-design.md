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

> **Revised 2026-07-15 after code verification.** Two items in this section were corrected once the implementation plan was researched against the code; the corrections are marked inline. See `docs/superpowers/plans/2026-07-15-orchestration-semantics.md` for the authoritative detail.

`packages/shared-schemas/schemas/job-request.schema.json`:

- New optional `job.model_settings`: a **flat map keyed by the target adapter's own setting keys**, values `string | boolean` — e.g. `{"model": "3.5 Flash", "thinking": "Extended"}` for Gemini, `{"mode": "Expert", "deepthink": true}` for DeepSeek. Omitted → the adapter's current operator defaults apply.
  - *Correction:* this section originally proposed `{ model?, thinking?, extras? }`. The code disproves that shape: DeepSeek has no `model` or `thinking` setting at all — its reasoning is a boolean **toggle** (`deepthink`) and its mode is a separate choice (`mode`). A fixed `model`/`thinking` abstraction would be false for DeepSeek and would need a per-provider translation table with no source of truth. Model labels are provider-specific regardless, so per-provider keys cost callers nothing and the catalog makes them discoverable.
- The existing `job.conversation_id` field becomes honored as an opaque conversation key scoped to `(tenant, app_id, target)`.
- New `job.options.conversation_missing`: `"fail"` (default) or `"restart"` — what to do when a bound thread no longer exists on the provider side.

`packages/openapi`: mirrors the same fields on job creation and documents `GET /v1/conversations`.

Stable error codes added — *correction:* the bare names originally listed here (`conversation_not_found` etc.) are invalid. `packages/shared-schemas/schemas/error.schema.json` pins `code` to `^UBAG-[A-Z0-9-]+-[0-9]{3}$` and its `category` enum is closed. The real codes, using existing categories only:

| Code | Category |
|---|---|
| `UBAG-VALIDATION-MODEL-UNAVAILABLE-001` | `validation` |
| `UBAG-VALIDATION-MODE-UNAVAILABLE-001` | `validation` |
| `UBAG-TARGET-CONVERSATION-NOT-FOUND-001` | `target` |
| `UBAG-TARGET-CONVERSATION-BROKEN-001` | `target` |

Adapter manifests (`adapters/*/manifest.json`) gain a `model_catalog` block: `settings: { [settingKey]: { kind: "choice" | "toggle", values?: string[] } }`, keyed to match `ProviderSetting.key` in `selectors.py`. Catalogs ship only labels proven by the current selector baseline (Gemini `model: ["3.5 Flash"]`, `thinking: ["Standard", "Extended"]`; DeepSeek `mode: ["Expert"]`, `deepthink` toggle); expanding them requires live-DOM verification per the repo's selector-drift rule. `chatgpt_web` ships an empty catalog (the worker deliberately leaves the account default); `mock` ships a synthetic one. `pnpm test:adapter-registry` validates the block. Callers discover catalogs through the existing `/v1/targets` / `/v1/adapters` endpoints.

The gateway validates `model_settings` against the target's catalog at job creation and fails fast. This is a **security control**, not only UX: the value is interpolated into a Playwright selector via `.format(value=desired)` in the worker's page driver.

Conformance fixtures (`packages/conformance`) and the TypeScript/Go SDKs are updated in the same slice, gated by `check:contracts` and `check:sdk-freshness`.

## Gateway conversation store

New package `apps/gateway/internal/conversations`, modeled on `apps/gateway/internal/alerts` (memory + SQLite + Postgres stores, nil-safe optional wiring):

- Row: `tenant, app_id, target, conversation_key, provider_thread_ref (chat URL/ID), state (active|broken), created_at, last_used_at, last_job_id`. `Bind` is an **upsert** on the full key — the engine retries an interaction up to 3×, so an append-only projection would duplicate.
- Migrations — *correction:* the numbers guessed here were wrong, and `migrations/sqlite/` is the **edge** tier, not where a gateway table belongs. Actual: `migrations/postgres/0010_conversations.sql` (mandatory, since Postgres `Ready()` asserts schema via `to_regclass` and fails closed) plus package-owned SQLite DDL bootstrapped in `Ready()`, following the `alerts` package exactly.
- Dispatch: when a job carries `conversation_id`, the gateway resolves it to a thread ref and injects it into the worker envelope through the existing executor dispatch boundary.
- Ingestion: the worker emits `conversation.thread_bound` / `conversation.thread_broken` / `conversation.thread_rebound` events; the existing `WorkerConsumer` intercepts them, forces the tenant from the job record, projects them into the store, and does **not** append them to the job's lifecycle event log — exactly the `browser.topology_reported` pattern. They are telemetry, so they are not added to the closed `type` enum in `job-event.schema.json`.
- New read-only route `GET /v1/conversations` (tenant/app-scoped, paginated, `job:read` RBAC, nil-safe 501 when disabled). `job:read` is deliberate: the RBAC action surface has drifted across `httpapi`, `grpcapi`, and `packages/security/src/rbac.ts` with no generator, so a bespoke `conversations:read` would mean three hand-edits for no benefit — conversations are job metadata.
- Env flag, inert by default — *correction:* `UBAG_CONVERSATIONS_ENABLED` (boolean, default `false`), matching the `UBAG_RATE_LIMIT_ENABLED` / `UBAG_CACHE_ENABLED` precedent. The store backend follows the existing `storeKind` thread (memory/sqlite/postgres) rather than a second enum, because that is how `alerts` already works. When disabled, `conversation_id` is accepted and ignored exactly as today.

## Worker / live engine

- **Model/mode selection needs no worker change** — *correction, verified 2026-07-15.* This section originally called for `ProviderConfig` to gain a `model_catalog`. There is no `ProviderConfig` class (the dataclasses are `ProviderSelectors` / `ProviderSetting`), and an override path **already exists**: `engine.py`'s `_resolve_provider_config` merges provider defaults < `UBAG_PROVIDER_CONFIG_<ID>` env JSON < `options.provider_config`, and `page_driver.ensure_provider_config` applies it at one line (`desired = overrides.get(setting.key, setting.desired)`). It was unreachable only because `job_options` is `additionalProperties: false` with no `provider_config` field, so no client could send it. The gateway therefore copies validated `model_settings` into the envelope's `options.provider_config`, and the worker works unchanged. The only worker hardening added is a value guard rejecting selector-breaking characters (defense in depth behind the gateway's catalog validation).
- `apps/worker/ubag_worker/live/engine.py` conversation flow:
  - **Resume:** navigate to the thread URL and verify it loaded (URL pattern + provider response container present) before typing. On failure, follow `conversation_missing`: `fail` (default) returns stable `conversation_not_found` and emits `conversation.thread_broken`; `restart` opens a fresh chat, rebinds the key, and emits `conversation.thread_rebound`.
  - **New conversation:** after the first response, capture the canonical chat URL (providers rewrite the URL on first message) and emit `conversation.thread_bound`.
  - **Redaction rule:** thread refs are chat URLs only — never cookies, storage state, or noVNC URLs (same posture as the topology intercept).
- `apps/worker/ubag_worker/orchestration/scheduler.py`: jobs sharing a conversation key run strictly FIFO; distinct conversations remain parallel under the existing AIMD channel caps.
- `apps/worker/ubag_worker/live/events.py`: the three conversation event types.
- The mock adapter honors `model_settings` and conversation binding deterministically so the entire path is CI-testable; live provider paths stay out of CI per existing ToS policy. *Note:* the registry-dispatched mock is `adapters/mock/ubag_mock_adapter/adapter.py` (`run(payload)`); the similarly-named `apps/worker/ubag_worker/adapters/mock/adapter.py` is a different, unreachable class implementing a `TargetAdapter` Protocol.

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
