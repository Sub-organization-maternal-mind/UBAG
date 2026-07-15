# Orchestration Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let callers pin a provider model/thinking-mode per job, and reuse a provider chat thread across jobs that share a conversation key.

**Architecture:** Contract-first extension of the existing job pipeline. `job.model_settings` becomes the public, catalog-validated surface over the worker's *already-working* `options.provider_config` override dict — the gateway validates and flattens, so the worker needs no model-settings code. Conversation affinity adds a new nil-safe `internal/conversations` gateway store (memory/SQLite/Postgres, modeled on `internal/alerts`), thread-ref injection into the worker envelope, and worker-emitted `conversation.*` events projected back by `WorkerConsumer`.

**Tech Stack:** Go 1.26 (gateway, stdlib-only + chi router), Python 3 (worker, Playwright live engine), JSON Schema 2020-12 + OpenAPI 3.1 (contracts), TypeScript (SDK/CLI/dashboard).

## Global Constraints

- **Safe mode is a hard product constraint** (`adapters/registry.json`): user-owned sessions only. No automated login, credential scraping/storage, or CAPTCHA solving. Conversation thread refs are **chat URLs only** — never cookies, storage state, or noVNC URLs.
- **Contracts first**: change `packages/openapi` / `packages/shared-schemas` before implementations. `pnpm check:blueprint` and `pnpm check:contracts` gate coverage.
- **Inert by default**: new runtime behavior ships behind `UBAG_CONVERSATIONS_ENABLED` (default `false`), matching the `UBAG_RATE_LIMIT_ENABLED` / `UBAG_CACHE_ENABLED` precedent. Flag off ⇒ byte-identical existing behavior.
- **No long builds during coding**: do targeted checks only (single Go package test, single pytest file, `pnpm lint:schemas`). Never run `pnpm check` / `pnpm test:v0:local` as part of routine implementation — the operator runs full verification.
- **Error codes MUST match** `^UBAG-[A-Z0-9-]+-[0-9]{3}$` and use a category already in the **closed** enum in `packages/shared-schemas/schemas/error.schema.json`. Do not extend the category enum.
- **Commit per task.** Small, flag-gated commits.

## Verified facts this plan depends on

These were confirmed by reading the code on 2026-07-15. Re-verify if the file moved; do not re-derive.

| Fact | Evidence |
|---|---|
| `options.provider_config` already overrides `ProviderSetting.desired` at one line: `desired = overrides.get(setting.key, setting.desired)` | `apps/worker/ubag_worker/live/page_driver.py` (`ensure_provider_config`, both `MockPageDriver` and `PlaywrightPageDriver`) |
| Override merge order: provider default < `UBAG_PROVIDER_CONFIG_<ID>` env JSON < `options.provider_config`. Flat dict. `_enabled`/`_new_chat` reserved. | `apps/worker/ubag_worker/live/engine.py` `_resolve_provider_config` |
| `job_options` is `additionalProperties: false` and has **no** `provider_config` ⇒ clients cannot legally send it today | `packages/shared-schemas/schemas/job-request.schema.json` `$defs.job_options` |
| `job.conversation_id` already exists in the contract, accepted and ignored | same file, `$defs.job` |
| `desired` is interpolated into a Playwright selector via `.format(value=desired)` ⇒ unvalidated values are an injection/drift surface | `page_driver.py` `_setting_satisfied` / `_apply_setting` |
| Gemini setting keys are `model` + `thinking`; DeepSeek's include `mode` | `apps/worker/ubag_worker/live/selectors.py` |
| Registry dispatches `adapters/mock/ubag_mock_adapter/adapter.py` (`run(payload)`), **not** `apps/worker/ubag_worker/adapters/mock/adapter.py` | `adapter_registry.py` `instantiate_adapter` + `events_for_payload`; traced live |
| `target=mock` takes the registry branch (mock is not in `PROVIDER_SELECTORS`) | `apps/worker/run_live_worker.py` |
| Postgres `Ready()` asserts schema via `to_regclass` and fails closed ⇒ PG table needs a migration file. SQLite packages self-bootstrap DDL in `Ready()`. | `apps/gateway/internal/alerts/postgres.go` / `sqlite.go` |
| Next free migration number: Postgres `0010`. `migrations/sqlite/*` is the **edge** tier — not where a gateway table goes. | `migrations/postgres/` (highest `0009_tenant_home_region.sql`) |
| `tools/check-contracts.mjs` hardcodes only migrations 0001-0003 ⇒ no registration needed for 0010 | `tools/check-contracts.mjs` ~L212-248 |

## Resolved design decisions

1. **`job.model_settings`** goes in `$defs.job` (sibling of `input`), NOT `job_options`. It is a **flat map keyed by the provider's own setting keys**, values `string | boolean`:
   - Gemini: `{"model": "3.5 Flash", "thinking": "Extended"}`
   - DeepSeek: `{"mode": "Expert", "deepthink": true}`

   **Rejected shape:** `{model, thinking, extras}`. The approved spec proposed it, but the code disproves it — DeepSeek has no `model` or `thinking` key at all; its reasoning is a **boolean toggle** (`deepthink`, `kind="toggle"`) and its mode is a separate choice (`mode`). A fixed `model`/`thinking` abstraction would be a lie for DeepSeek and would need a per-provider translation table with no source of truth. Provider model labels are provider-specific anyway ("3.5 Flash" is meaningless to DeepSeek), so per-provider keys cost the caller nothing and the catalog makes them discoverable via `/v1/adapters`.
2. **Gateway passes `model_settings` through** as the worker envelope's `options.provider_config` — the shapes are now identical by construction, so this is a copy, not a translation. `provider_config` stays an internal worker-protocol detail, never client-settable directly. **Worker needs no model-settings change.**
2b. **Catalog values ship only where verified in code.** Per the repo gotcha ("web-adapter DOM selectors are brittle and get re-baselined against the live pages"), the initial catalogs contain only labels proven by the current selector baselines: Gemini `model: ["3.5 Flash"]`, `thinking: ["Standard", "Extended"]`; DeepSeek `mode: ["Expert"]`, `deepthink` (toggle). Additional labels are added **only** after live-DOM verification by the operator. A small honest catalog beats a large invented one — an unverified label fails `_setting_satisfied` three times and raises `DriftDetectedError`.
3. **Error codes** (existing categories only):
   - `UBAG-VALIDATION-MODEL-UNAVAILABLE-001` — category `validation`
   - `UBAG-VALIDATION-MODE-UNAVAILABLE-001` — category `validation`
   - `UBAG-TARGET-CONVERSATION-NOT-FOUND-001` — category `target`
   - `UBAG-TARGET-CONVERSATION-BROKEN-001` — category `target`
4. **Flag**: `UBAG_CONVERSATIONS_ENABLED` (default false). Store backend follows the existing `storeKind` thread (memory/sqlite/postgres) exactly like `alerts`. Off ⇒ `Config.Conversations` nil ⇒ route 501 + no envelope injection + `conversation_id` ignored as today.
5. **RBAC**: `job:read`. Chosen deliberately — viewer already holds it, and the RBAC action surface has drifted across `httpapi`, `grpcapi`, and `packages/security/src/rbac.ts` with **no generator**; a new `conversations:read` would need three hand-edits. Conversations are job metadata.
6. **Conversation events are telemetry, not lifecycle**: `conversation.thread_bound` / `.thread_broken` / `.thread_rebound` are intercepted by `WorkerConsumer` and projected into the store, then `continue` — following the `browser.topology_reported` precedent. Do **not** add them to the closed `type` enum in `job-event.schema.json`.
7. **Store is upsert-by-key** (`Bind`), not append — the engine retries an interaction up to 3× and a naive append would duplicate.
8. **Response shape**: typed `ConversationListResponse` (mirrors `AlertListResponse`), avoiding the closed `CollectionResponse.kind` enum.
9. **SQLite DDL self-bootstraps** in `Ready()` (alerts pattern). Postgres ships `migrations/postgres/0010_conversations.sql`. No `migrations/sqlite/` file (that is the edge tier).

## File structure

**Create:**
- `apps/gateway/internal/conversations/conversations.go` — domain type, `Key`, `Filter`, `Store` interface, `MemoryStore`, `Manager`
- `apps/gateway/internal/conversations/sqlite.go` — `SQLiteStore` + self-owned DDL + `Ready()` bootstrap
- `apps/gateway/internal/conversations/postgres.go` — `PostgresStore` + `to_regclass` readiness
- `apps/gateway/internal/conversations/conversations_test.go` — in-package tests (alerts idiom)
- `migrations/postgres/0010_conversations.sql`
- `apps/dashboard/src/routes/conversations/+page.svelte` (+ loader, mirroring the alerts page layout)

**Modify:**
- `packages/shared-schemas/schemas/job-request.schema.json` — `$defs.job.model_settings`, `$defs.job_options.conversation_missing`
- `packages/shared-schemas/errors.json` — 4 new catalog codes
- `packages/openapi/openapi.yaml` — job-create body, `GET /v1/conversations`, `ConversationListResponse`
- `adapters/{gemini_web,deepseek_web,chatgpt_web,mock}/manifest.json` — `model_catalog` block
- `packages/adapter-registry/**` — `model_catalog` validation + index surfacing
- `apps/gateway/internal/jobcore/jobcore.go` — allow-list `model_settings`; shared `ValidateModelSettings`
- `apps/gateway/internal/httpapi/server.go` — createJob + processBatchEntry validation, `GET /v1/conversations`, Config field
- `apps/gateway/internal/grpcapi/server.go` — CreateJob validation parity
- `apps/gateway/internal/serve/serve.go` — flag + store construction
- `apps/gateway/internal/executor/executor.go` — envelope `provider_config` + `conversation` injection
- `apps/gateway/internal/executor/workerconsumer.go` (exact name per repo) — conversation event interception
- `apps/worker/ubag_worker/live/engine.py` — conversation resume/bind/restart, value sanity guard
- `apps/worker/ubag_worker/live/page_driver.py` — thread-URL capture helper
- `apps/worker/ubag_worker/live/events.py` — 3 event names
- `apps/worker/ubag_worker/orchestration/scheduler.py` — FIFO per conversation key
- `adapters/mock/ubag_mock_adapter/adapter.py` — honor model_settings + conversation binding
- `packages/sdk-typescript/src/types.ts`, `packages/sdk-go/**` — new fields; regenerate manifests
- `packages/cli/**` — `--model` / `--thinking` / `--conversation` flags
- `packages/conformance/fixtures/v0/scenarios.json` + `scripts/validate-fixtures.mjs` — new scenarios + `requiredEndpointIds`

---

## Phase A — Contracts

### Task A1: Add `model_settings` + `conversation_missing` to the job request schema

**Files:**
- Modify: `packages/shared-schemas/schemas/job-request.schema.json`

**Interfaces:**
- Produces: `job.model_settings` = flat map `{[settingKey: string]: string | boolean}`; `job.options.conversation_missing = "fail" | "restart"` (default `"fail"`).

- [ ] **Step 1: Add `model_settings` to `$defs.job.properties`** (after `template_id`, before `input`). `$defs.job` is `additionalProperties: false`, so this is required for the field to be legal. The `propertyNames` pattern blocks `_`-prefixed keys, which are reserved worker control keys (`_enabled`, `_new_chat`) that a client must never be able to set.

```json
"model_settings": {
  "type": ["object", "null"],
  "description": "Optional per-job provider UI settings, keyed by the target adapter's own setting keys (e.g. gemini_web: model, thinking; deepseek_web: mode, deepthink). Validated against the target adapter's model_catalog at job creation; discover the available keys and values from /v1/adapters. Omitted means the adapter's operator defaults apply. Must not contain credentials or secrets.",
  "propertyNames": {
    "pattern": "^[a-z][a-z0-9_]*$",
    "maxLength": 64
  },
  "additionalProperties": {
    "type": ["string", "boolean"],
    "maxLength": 96
  },
  "maxProperties": 16,
  "examples": [
    { "model": "3.5 Flash", "thinking": "Extended" },
    { "mode": "Expert", "deepthink": true }
  ]
}
```

- [ ] **Step 2: Add `conversation_missing` to `$defs.job_options.properties`** (after `cache_policy`).

```json
"conversation_missing": {
  "type": "string",
  "enum": ["fail", "restart"],
  "default": "fail",
  "description": "Behavior when job.conversation_id refers to a provider chat thread that no longer exists. fail returns UBAG-TARGET-CONVERSATION-NOT-FOUND-001; restart opens a fresh chat and rebinds the key."
}
```

- [ ] **Step 3: Update the `job.conversation_id` description** to state it is honored (it currently has no description).

```json
"conversation_id": {
  "type": ["string", "null"],
  "minLength": 1,
  "maxLength": 160,
  "description": "Opaque caller-owned conversation key scoped to (tenant, app_id, target). Reused keys resume the same provider chat thread; unseen keys open a new chat. Ignored unless the gateway has conversations enabled. Must not contain credentials or secrets.",
  "examples": ["conv_123"]
}
```

- [ ] **Step 4: Validate the schema**

Run: `cmd /c pnpm lint:schemas`
Expected: PASS (exit 0).

- [ ] **Step 5: Commit**

```bash
git add packages/shared-schemas/schemas/job-request.schema.json
git commit -m "feat(contracts): add job.model_settings and options.conversation_missing"
```

### Task A2: Register the four error codes

**Files:**
- Modify: `packages/shared-schemas/errors.json`

**Interfaces:**
- Produces: catalog codes `UBAG-VALIDATION-MODEL-UNAVAILABLE-001`, `UBAG-VALIDATION-MODE-UNAVAILABLE-001`, `UBAG-TARGET-CONVERSATION-NOT-FOUND-001`, `UBAG-TARGET-CONVERSATION-BROKEN-001`.

- [ ] **Step 1: Read `packages/shared-schemas/errors.json`** and locate `x-catalog.namespaces[]` entries with `prefix: "UBAG-VALIDATION"` and `prefix: "UBAG-TARGET"`. Match the existing `{code, message, retryable}` object shape exactly.

- [ ] **Step 2: Append to the `UBAG-VALIDATION` namespace `codes[]`**

```json
{
  "code": "UBAG-VALIDATION-MODEL-UNAVAILABLE-001",
  "message": "Requested model is not in the target adapter's model catalog.",
  "retryable": false
},
{
  "code": "UBAG-VALIDATION-MODE-UNAVAILABLE-001",
  "message": "Requested thinking mode or extra setting is not in the target adapter's model catalog.",
  "retryable": false
}
```

- [ ] **Step 3: Append to the `UBAG-TARGET` namespace `codes[]`**

```json
{
  "code": "UBAG-TARGET-CONVERSATION-NOT-FOUND-001",
  "message": "The bound provider chat thread for this conversation no longer exists.",
  "retryable": false,
  "note": "Set job.options.conversation_missing=restart to open a fresh chat and rebind the key instead of failing."
},
{
  "code": "UBAG-TARGET-CONVERSATION-BROKEN-001",
  "message": "The conversation binding is marked broken and cannot be resumed.",
  "retryable": false
}
```

- [ ] **Step 4: Verify codes match the regex and categories are already in the closed enum**

Run: `cmd /c pnpm lint:schemas` then `node tools/check-contracts.mjs`
Expected: both exit 0. (`validation` and `target` are already in `error.schema.json`'s category enum — do not touch that enum.)

- [ ] **Step 5: Commit**

```bash
git add packages/shared-schemas/errors.json
git commit -m "feat(contracts): register conversation and model-catalog error codes"
```

### Task A3: Add `model_catalog` to adapter manifests

**Files:**
- Modify: `adapters/gemini_web/manifest.json`, `adapters/deepseek_web/manifest.json`, `adapters/chatgpt_web/manifest.json`, `adapters/mock/manifest.json`

**Interfaces:**
- Produces: manifest block `model_catalog: { settings: { [settingKey]: { kind: "choice" | "toggle", values?: string[] } } }`. `settings: {}` means "nothing is caller-selectable; the operator default always applies".
- Catalog keys MUST exactly equal `ProviderSetting.key` in `selectors.py`, and `values` MUST be labels the current selector baseline can satisfy — they are interpolated into Playwright selectors via `.format(value=desired)`.

- [ ] **Step 1: Verify the keys and labels against `apps/worker/ubag_worker/live/selectors.py`** before writing. As of 2026-07-15 the verified baselines are: `GEMINI_WEB.settings` = `model` (choice, desired `"3.5 Flash"`) and `thinking` (choice, desired `"Extended"`, submenu offers `Standard` / `Extended` per the inline comment); `DEEPSEEK_WEB.settings` = `mode` (choice, desired `"Expert"`) and `deepthink` (**toggle**, desired `True`). If these have been re-baselined since, use what the file says — never invent labels.

- [ ] **Step 2: Insert `model_catalog` after `capabilities`** in each manifest, preserving each file's existing key order and 2-space indentation.

`adapters/gemini_web/manifest.json`:

```json
"model_catalog": {
  "settings": {
    "model": { "kind": "choice", "values": ["3.5 Flash"] },
    "thinking": { "kind": "choice", "values": ["Standard", "Extended"] }
  }
},
```

`adapters/deepseek_web/manifest.json`:

```json
"model_catalog": {
  "settings": {
    "mode": { "kind": "choice", "values": ["Expert"] },
    "deepthink": { "kind": "toggle" }
  }
},
```

Only labels proven by the current selector baseline are listed. The Gemini mode picker and DeepSeek mode pills almost certainly offer more options, but enumerating them requires live-DOM verification (repo gotcha: selectors are brittle and get re-baselined against live pages). Shipping an unverified label would produce a `DriftDetectedError` at runtime, which is worse than not offering it. Expanding these lists is an operator task after live verification.

- [ ] **Step 3: `chatgpt_web` gets an empty catalog.** `selectors.py` deliberately leaves the ChatGPT account default (see its inline comment: only start a fresh chat, do not touch model/mode). An empty `settings` object makes "not caller-selectable" explicit rather than accidental, and makes any `model_settings` for ChatGPT fail fast with a clear error instead of silently doing nothing.

```json
"model_catalog": {
  "settings": {}
},
```

- [ ] **Step 4: `mock` gets a synthetic catalog** covering both kinds so the full path is CI-testable without a browser.

```json
"model_catalog": {
  "settings": {
    "model": { "kind": "choice", "values": ["mock-fast", "mock-deep"] },
    "thinking": { "kind": "choice", "values": ["standard", "extended"] },
    "deepthink": { "kind": "toggle" }
  }
},
```

- [ ] **Step 5: Verify the registry still loads**

Run: `cmd /c pnpm test:adapter-registry`
Expected: PASS. (Python `validate_manifest` uses a field whitelist and the TS `adapter-manifest.schema.json` is `additionalProperties: true`, so an unknown block passes today — Task A4 makes it validated.)

- [ ] **Step 6: Commit**

```bash
git add adapters/gemini_web/manifest.json adapters/deepseek_web/manifest.json adapters/chatgpt_web/manifest.json adapters/mock/manifest.json
git commit -m "feat(adapters): declare model_catalog per adapter manifest"
```

### Task A4: Validate `model_catalog` in the adapter registry

**Files:**
- Modify: `packages/adapter-registry/schemas/adapter-manifest.schema.json`
- Modify: `packages/adapter-registry/schemas/registry-index.schema.json`
- Modify: `packages/adapter-registry/src/registry.ts` (`buildRegistryEntry`)
- Test: the existing adapter-registry test file (find it under `packages/adapter-registry/`)

**Interfaces:**
- Consumes: the `model_catalog` block from Task A3.
- Produces: `buildRegistryEntry` output carries `modelCatalog` so `/v1/adapters` can surface it.

- [ ] **Step 1: Read `packages/adapter-registry/src/json-schema.ts`** and list the draft-07 keywords the hand-rolled validator actually supports. It does **not** support `$ref`, `oneOf`, or `maxLength`. Write the `model_catalog` schema using only supported keywords.

- [ ] **Step 2: Write the failing test.** Add a case asserting a manifest with a `model_catalog` whose `models` contains a non-string is rejected, and a valid one is accepted and surfaced on the registry entry.

- [ ] **Step 3: Run it and confirm it fails**

Run: `cmd /c pnpm test:adapter-registry`
Expected: FAIL.

- [ ] **Step 4: Add `model_catalog` to `adapter-manifest.schema.json`** using only supported keywords, then add the surfaced field to `registry-index.schema.json` (it is `additionalProperties: false` per entry, so this edit is mandatory) and populate it in `buildRegistryEntry`.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cmd /c pnpm test:adapter-registry`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/adapter-registry
git commit -m "feat(adapter-registry): validate and surface model_catalog"
```

### Task A5: OpenAPI — job-create fields, `GET /v1/conversations`, `ConversationListResponse`

**Files:**
- Modify: `packages/openapi/openapi.yaml`

**Interfaces:**
- Produces: `GET /v1/conversations` returning `ConversationListResponse`; job-create body carrying `model_settings` + `options.conversation_missing`.

- [ ] **Step 1: Mirror `model_settings` and `conversation_missing`** into the job-create request body schema, matching the JSON Schema from Task A1 field-for-field.

- [ ] **Step 2: Read the existing `GET /v1/alerts` path and `AlertListResponse` schema** and copy their structure exactly (pagination params, envelope, security, error responses).

- [ ] **Step 3: Add `GET /v1/conversations`** with the same pagination params as `/v1/alerts`, `job:read` scope, and responses `200` (`ConversationListResponse`), `401`, `403`, `501`.

- [ ] **Step 4: Add `ConversationListResponse`** to `components.schemas`, modeled on `AlertListResponse`. A conversation item is:

```yaml
ConversationListResponse:
  type: object
  additionalProperties: false
  required: [api_version, conversations]
  properties:
    api_version: { type: string }
    conversations:
      type: array
      items:
        type: object
        additionalProperties: false
        required: [tenant_id, app_id, target, conversation_key, state, created_at, last_used_at]
        properties:
          tenant_id: { type: string }
          app_id: { type: string }
          target: { type: string }
          conversation_key: { type: string }
          provider_thread_ref: { type: string, description: "Provider chat URL. Never contains session or credential material." }
          state: { type: string, enum: [active, broken] }
          created_at: { type: string, format: date-time }
          last_used_at: { type: string, format: date-time }
          last_job_id: { type: string }
    next_cursor: { type: [string, "null"] }
```

- [ ] **Step 5: Lint**

Run: `cmd /c pnpm lint:openapi` and `node tools/check-contracts.mjs`
Expected: both exit 0.

- [ ] **Step 6: Commit**

```bash
git add packages/openapi/openapi.yaml
git commit -m "feat(openapi): model_settings, conversation_missing, GET /v1/conversations"
```

### Task A6: Conformance fixtures

**Files:**
- Modify: `packages/conformance/fixtures/v0/scenarios.json`
- Modify: `packages/conformance/scripts/validate-fixtures.mjs`

- [ ] **Step 1: Read `validate-fixtures.mjs`** and note `requiredEndpointIds` (~line 27) and the exact scenario object shape. Read one existing job-create scenario and one collection scenario as templates.

- [ ] **Step 2: Add two job-create scenarios** matching the existing shape exactly. Both reuse the harness's existing `POST /v1/jobs` → `createJob` dispatch, which forwards the full scenario body, so they are safe before the SDK types land:
  1. job create with `model_settings: {"model": "mock-deep", "thinking": "extended"}` + `conversation_id` → `202` accepted (`expect.ok`).
  2. job create with `model_settings: {"model": "does-not-exist"}` → `409`/`400` error envelope with `UBAG-VALIDATION-MODEL-UNAVAILABLE-001` (`expect.throws: "UbagApiError"`, `error.code`).

- [ ] **Step 3: Do NOT add a `conversations.list.ok` scenario here.** The SDK conformance harnesses (`packages/sdk-typescript/test/conformance.test.mjs` `invokeScenario`, and the Go equivalent) dispatch each scenario to a **named client method** and throw on an unmapped route. A `GET /v1/conversations` scenario therefore cannot exist until the SDK has `listConversations` — that scenario, its `requiredEndpointIds` entry, and the harness dispatch lines are all added together in **Task D1a** (moved out of this task to keep every commit green).

- [ ] **Step 4: Validate**

Run: `node packages/conformance/scripts/validate-fixtures.mjs`
Expected: exit 0.

- [ ] **Step 5: Regenerate SDK contract manifests.** Touching `job-request.schema.json` changes its sha256 and hard-fails freshness until regenerated.

Run: `cmd /c pnpm generate:sdk-contracts` then `cmd /c pnpm check:sdk-freshness`
Expected: freshness exits 0. (If the script name differs, read `package.json` `scripts` for the `generate:*` entry.)

- [ ] **Step 6: Commit**

```bash
git add packages/conformance packages/sdk-typescript packages/sdk-go
git commit -m "test(conformance): scenarios for model_settings, conversation affinity, conversations list"
```

---

## Phase B — Gateway

### Task B1: `internal/conversations` package — types, Store interface, MemoryStore

**Files:**
- Create: `apps/gateway/internal/conversations/conversations.go`
- Create: `apps/gateway/internal/conversations/conversations_test.go`

**Interfaces:**
- Produces (later tasks depend on these exact signatures):

```go
package conversations

const (
    StateActive = "active"
    StateBroken = "broken"
)

// Key identifies one conversation binding.
type Key struct {
    TenantID        string
    AppID           string
    Target          string
    ConversationKey string
}

// Conversation is a durable binding from a caller conversation key to a
// provider chat thread. ProviderThreadRef is a chat URL only — never cookies,
// storage state, or noVNC URLs.
type Conversation struct {
    TenantID          string    `json:"tenant_id"`
    AppID             string    `json:"app_id"`
    Target            string    `json:"target"`
    ConversationKey   string    `json:"conversation_key"`
    ProviderThreadRef string    `json:"provider_thread_ref,omitempty"`
    State             string    `json:"state"`
    CreatedAt         time.Time `json:"created_at"`
    LastUsedAt        time.Time `json:"last_used_at"`
    LastJobID         string    `json:"last_job_id,omitempty"`
}

type Filter struct {
    TenantID string
    AppID    string // optional; empty means any app
    Target   string // optional; empty means any target
    Limit    int    // 0 means no limit
}

type Store interface {
    Ready(ctx context.Context) error
    Resolve(ctx context.Context, key Key) (Conversation, bool, error)
    // Bind upserts by Key. Re-binding an existing key overwrites
    // ProviderThreadRef, sets State=active, and refreshes LastUsedAt.
    Bind(ctx context.Context, conv Conversation) (Conversation, error)
    MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error)
    Touch(ctx context.Context, key Key, jobID string, at time.Time) error
    List(ctx context.Context, filter Filter) ([]Conversation, error)
}

func NewMemoryStore() *MemoryStore
func NewManager(store Store, logger *slog.Logger, storeKind string) *Manager
func (m *Manager) Resolve(ctx context.Context, key Key) (Conversation, bool, error)
func (m *Manager) Bind(ctx context.Context, conv Conversation) (Conversation, error)
func (m *Manager) MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error)
func (m *Manager) Touch(ctx context.Context, key Key, jobID string, at time.Time) error
func (m *Manager) List(ctx context.Context, filter Filter) ([]Conversation, error)
```

- [ ] **Step 1: Read `apps/gateway/internal/alerts/alerts.go` in full.** Mirror its idioms exactly: `ctx` first; single-row reads return `(T, bool, error)` with **not-found never an error**; every method guards `if s == nil || s.db == nil`; `List` sorts a copy and truncates to `Limit`; `MemoryStore` is a tenant-keyed map + `sync.Mutex`; `NewManager` normalizes a nil logger to `slog.Default()`. Tests are **in-package** (`package conversations`).

- [ ] **Step 2: Write the failing tests** in `conversations_test.go`:

```go
func TestMemoryStoreBindIsUpsertByKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()

	first, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/1",
		State: StateActive, CreatedAt: now, LastUsedAt: now,
	})
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if first.ProviderThreadRef != "https://example/chat/1" {
		t.Fatalf("thread ref = %q", first.ProviderThreadRef)
	}

	// Re-binding the same key must overwrite, not append.
	if _, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/2",
		State: StateActive, CreatedAt: now, LastUsedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("second bind: %v", err)
	}

	got, found, err := store.Resolve(ctx, key)
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if got.ProviderThreadRef != "https://example/chat/2" {
		t.Fatalf("thread ref after rebind = %q, want chat/2", got.ProviderThreadRef)
	}

	all, err := store.List(ctx, Filter{TenantID: "t1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(list) = %d, want 1 (upsert must not append)", len(all))
	}
}

func TestMemoryStoreResolveIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Unix(1, 0).UTC()
	if _, err := store.Bind(ctx, Conversation{
		TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1",
		ProviderThreadRef: "https://example/chat/1", State: StateActive,
		CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, found, err := store.Resolve(ctx, Key{
		TenantID: "t2", AppID: "a1", Target: "mock", ConversationKey: "c1",
	}); err != nil || found {
		t.Fatalf("cross-tenant resolve: found=%v err=%v, want found=false err=nil", found, err)
	}
}

func TestMemoryStoreMarkBroken(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()
	if _, err := store.Bind(ctx, Conversation{
		TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
		ConversationKey: key.ConversationKey, ProviderThreadRef: "https://example/chat/1",
		State: StateActive, CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, found, err := store.MarkBroken(ctx, key, now.Add(time.Minute))
	if err != nil || !found {
		t.Fatalf("mark broken: found=%v err=%v", found, err)
	}
	if got.State != StateBroken {
		t.Fatalf("state = %q, want %q", got.State, StateBroken)
	}
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/conversations/...`
Expected: FAIL (package does not compile / does not exist).

- [ ] **Step 4: Implement `conversations.go`** with the interfaces above. `MemoryStore` keeps `map[string][]Conversation` keyed by tenant (alerts idiom) but `Bind` **must find-and-replace by full `Key`** before appending. `Manager` wraps the store, normalizes nil logger, and exposes the same methods plus a `ConfigSummary`-style `StoreKind` for the readiness/config surface.

- [ ] **Step 5: Run to verify passing**

Run: `cd apps/gateway && go test ./internal/conversations/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/gateway/internal/conversations
git commit -m "feat(gateway): conversations domain types, Store interface, memory store"
```

### Task B2: SQLite + Postgres conversation stores + migration

**Files:**
- Create: `apps/gateway/internal/conversations/sqlite.go`
- Create: `apps/gateway/internal/conversations/postgres.go`
- Create: `migrations/postgres/0010_conversations.sql`
- Modify: `apps/gateway/internal/conversations/conversations_test.go`

**Interfaces:**
- Consumes: `Store`, `Conversation`, `Key`, `Filter` from Task B1.
- Produces: `func NewSQLiteStore(db *sql.DB) *SQLiteStore`, `func NewPostgresStore(db *sql.DB) *PostgresStore`.

- [ ] **Step 1: Read `apps/gateway/internal/alerts/sqlite.go` and `postgres.go` in full.** Copy the asymmetry exactly: SQLite `Ready()` is **creative** (ping, then `Exec` each `CREATE TABLE/INDEX IF NOT EXISTS`); Postgres `Ready()` is **assertive** (ping, then `requireConversationsObject` via `to_regclass`, never creates). SQLite timestamps are `TEXT NOT NULL DEFAULT ''` empty-string sentinels via `canonicalTime`/`parseCanonicalTime`; Postgres uses nullable `TIMESTAMPTZ` + `sql.NullTime`. Constructors take an already-open `*sql.DB`, never open/close, and cannot fail (no `error` return).

- [ ] **Step 2: Write the failing SQLite test** (uses `modernc.org/sqlite` in-memory, matching the alerts test idiom — check how the alerts tests open a DB and reuse that exactly).

```go
func TestSQLiteStoreBindResolveUpsert(t *testing.T) {
	ctx := context.Background()
	db := newTestSQLiteDB(t) // reuse the alerts test helper idiom
	store := NewSQLiteStore(db)
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	key := Key{TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1"}
	now := time.Unix(1, 0).UTC()
	for _, ref := range []string{"https://example/chat/1", "https://example/chat/2"} {
		if _, err := store.Bind(ctx, Conversation{
			TenantID: key.TenantID, AppID: key.AppID, Target: key.Target,
			ConversationKey: key.ConversationKey, ProviderThreadRef: ref,
			State: StateActive, CreatedAt: now, LastUsedAt: now,
		}); err != nil {
			t.Fatalf("bind %s: %v", ref, err)
		}
	}
	got, found, err := store.Resolve(ctx, key)
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if got.ProviderThreadRef != "https://example/chat/2" {
		t.Fatalf("thread ref = %q, want chat/2", got.ProviderThreadRef)
	}
	all, err := store.List(ctx, Filter{TenantID: "t1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(all))
	}
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/conversations/... -run TestSQLiteStore`
Expected: FAIL.

- [ ] **Step 4: Implement `sqlite.go`** with self-owned DDL. Primary key is the full conversation key so `Bind` is a real upsert:

```go
const sqliteCreateConversationsTable = `
CREATE TABLE IF NOT EXISTS gateway_conversations (
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	target TEXT NOT NULL,
	conversation_key TEXT NOT NULL,
	provider_thread_ref TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'active',
	created_at TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	last_job_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, app_id, target, conversation_key)
)`

const sqliteCreateConversationsTenantIndex = `
CREATE INDEX IF NOT EXISTS idx_gateway_conversations_tenant_used
	ON gateway_conversations (tenant_id, last_used_at)`
```

`Bind` uses `INSERT ... ON CONFLICT (tenant_id, app_id, target, conversation_key) DO UPDATE SET provider_thread_ref=excluded.provider_thread_ref, state=excluded.state, last_used_at=excluded.last_used_at, last_job_id=excluded.last_job_id`.

- [ ] **Step 5: Implement `postgres.go`** — same columns, `TIMESTAMPTZ`, `ON CONFLICT ... DO UPDATE`, and:

```go
func requireConversationsObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
```

- [ ] **Step 6: Create `migrations/postgres/0010_conversations.sql`.** Mandatory — Postgres `Ready()` fails closed without it. Follow the `0007_alerts.sql` style (header comment explaining intent, `CREATE TABLE IF NOT EXISTS`, indexes, ledger footer).

```sql
-- Migration 0010: conversations
-- Durable bindings from a caller-owned conversation key to a provider chat
-- thread. Apply after 0009_tenant_home_region.sql.
--
-- A conversation key is opaque and caller-owned, scoped to
-- (tenant_id, app_id, target). Reused keys resume the same provider chat so the
-- end user keeps their context; unseen keys open a new chat.
--
-- provider_thread_ref holds a provider chat URL ONLY. No cookies, storage
-- state, credentials, or noVNC URLs are ever stored here — resuming a chat is a
-- navigation inside an already user-authenticated session.

CREATE TABLE IF NOT EXISTS gateway_conversations (
  tenant_id           TEXT        NOT NULL,
  app_id              TEXT        NOT NULL,
  target              TEXT        NOT NULL,
  conversation_key    TEXT        NOT NULL,
  provider_thread_ref TEXT        NOT NULL DEFAULT '',
  state               TEXT        NOT NULL DEFAULT 'active',
  created_at          TIMESTAMPTZ NOT NULL,
  last_used_at        TIMESTAMPTZ,
  last_job_id         TEXT        NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, app_id, target, conversation_key)
);

CREATE INDEX IF NOT EXISTS idx_gateway_conversations_tenant_used
  ON gateway_conversations (tenant_id, last_used_at DESC);

CREATE INDEX IF NOT EXISTS idx_gateway_conversations_state
  ON gateway_conversations (tenant_id, state);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0010', 'conversations', '', now())
ON CONFLICT (version) DO NOTHING;
```

- [ ] **Step 7: Run tests**

Run: `cd apps/gateway && go test ./internal/conversations/...`
Expected: PASS. (Postgres tests stay env-gated and skip without `UBAG_TEST_POSTGRES_URL` — copy the alerts skip idiom.)

- [ ] **Step 8: Commit**

```bash
git add apps/gateway/internal/conversations migrations/postgres/0010_conversations.sql
git commit -m "feat(gateway): sqlite + postgres conversation stores and migration 0010"
```

### Task B3: Model-catalog validation in `jobcore`, wired into every create path

**Files:**
- Modify: `apps/gateway/internal/jobcore/jobcore.go`
- Modify: `apps/gateway/internal/httpapi/server.go` (createJob **and** `processBatchEntry`)
- Modify: `apps/gateway/internal/grpcapi/server.go` (`CreateJob`)
- Test: `apps/gateway/internal/jobcore/jobcore_test.go` (or the existing test file there)

**Interfaces:**
- Produces:

```go
// CatalogSetting declares one caller-selectable provider UI setting.
type CatalogSetting struct {
    Kind   string   `json:"kind"`             // "choice" | "toggle"
    Values []string `json:"values,omitempty"` // choice only
}

// ModelCatalog mirrors the adapter manifest model_catalog block.
type ModelCatalog struct {
    Settings map[string]CatalogSetting `json:"settings"`
}

func ValidateModelSettings(target string, settings map[string]any, catalog ModelCatalog) error
```

  returning a typed error carrying `UBAG-VALIDATION-MODEL-UNAVAILABLE-001` (unknown/absent `model` value) or `UBAG-VALIDATION-MODE-UNAVAILABLE-001` (unknown setting key, or bad value for any non-`model` setting).
- Produces: `job.model_settings` added to the payload-safety allow-list so it is secret-scanned.

- [ ] **Step 1: Read `apps/gateway/internal/jobcore/jobcore.go` (`ValidatePayload`, ~L84-103) and `apps/gateway/internal/payloadpolicy/policy.go` in full.** `ValidatePayload` builds an **explicit allow-list map** of job fields — a field absent from it is silently **not** secret-scanned. Note the disallowed secret segments (`token`, `session`, …) because a catalog `extras` key containing one will be rejected.

- [ ] **Step 2: Enumerate the create paths.** Read `createJob`, `processBatchEntry` (~L1188), and `grpcapi.CreateJob` (~L84). `processBatchEntry` re-implements validation inline and skips `applyTemplateForCreate`. Record every path that must call the new validator; there are expected to be three.

- [ ] **Step 3: Write the failing test** in the `jobcore` package:

```go
func mockCatalog() ModelCatalog {
	return ModelCatalog{Settings: map[string]CatalogSetting{
		"model":     {Kind: "choice", Values: []string{"mock-fast", "mock-deep"}},
		"thinking":  {Kind: "choice", Values: []string{"standard", "extended"}},
		"deepthink": {Kind: "toggle"},
	}}
}

func TestValidateModelSettingsAcceptsCatalogValues(t *testing.T) {
	settings := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	if err := ValidateModelSettings("mock", settings, mockCatalog()); err != nil {
		t.Fatalf("want nil for in-catalog settings, got %v", err)
	}
}

func TestValidateModelSettingsRejectsUnknownModelValue(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"model": "gpt-nonexistent"}, mockCatalog())
	if err == nil {
		t.Fatal("want error for out-of-catalog model, got nil")
	}
	if !strings.Contains(err.Error(), "UBAG-VALIDATION-MODEL-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODEL-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsUnknownSettingKey(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"nope": "x"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsBadThinkingValue(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"thinking": "ludicrous"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsStringForToggle(t *testing.T) {
	// deepthink is kind=toggle: the worker passes the value to a boolean
	// comparison, so a string here would silently mean "truthy".
	err := ValidateModelSettings("mock", map[string]any{"deepthink": "yes"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsBoolForChoice(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"model": true}, mockCatalog())
	if err == nil {
		t.Fatal("want error for boolean value on a choice setting, got nil")
	}
}

func TestValidateModelSettingsEmptyCatalogRejectsAnySetting(t *testing.T) {
	// chatgpt_web ships an empty catalog: nothing is caller-selectable, so a
	// request that thinks it is picking a model must fail loudly rather than
	// be silently ignored.
	err := ValidateModelSettings("chatgpt_web", map[string]any{"model": "anything"}, ModelCatalog{})
	if err == nil {
		t.Fatal("want error when the catalog is empty, got nil")
	}
}

func TestValidateModelSettingsNilIsAllowed(t *testing.T) {
	// Omitted model_settings must keep today's operator defaults.
	if err := ValidateModelSettings("mock", nil, ModelCatalog{}); err != nil {
		t.Fatalf("want nil for absent settings, got %v", err)
	}
}

func TestValidateModelSettingsRejectsReservedKey(t *testing.T) {
	// _enabled / _new_chat are reserved worker control keys. The schema pattern
	// blocks them at the edge; this is defense in depth for gRPC/batch paths.
	err := ValidateModelSettings("mock", map[string]any{"_enabled": "false"}, mockCatalog())
	if err == nil {
		t.Fatal("want error for reserved _-prefixed key, got nil")
	}
}
```

- [ ] **Step 4: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/jobcore/... -run TestValidateModelSettings`
Expected: FAIL (undefined `ValidateModelSettings`).

- [ ] **Step 5: Implement `ValidateModelSettings` + `ModelCatalog` + `CatalogSetting`** in `jobcore.go`. Rules:
  - nil/empty settings → nil error (operator defaults apply).
  - every key must exist in `catalog.Settings`, else `UBAG-VALIDATION-MODE-UNAVAILABLE-001`.
  - any `_`-prefixed key → error (reserved worker control keys).
  - `kind: "choice"` → value must be a `string` present in `Values`. A bad value for key `model` returns `UBAG-VALIDATION-MODEL-UNAVAILABLE-001`; for any other choice key, `UBAG-VALIDATION-MODE-UNAVAILABLE-001`.
  - `kind: "toggle"` → value must be a `bool`, else `UBAG-VALIDATION-MODE-UNAVAILABLE-001`.
  - empty catalog + any provided setting → error.

  **This validator is a security control**, not just UX: the value is interpolated into a Playwright selector via `.format(value=desired)` in `page_driver.py`.

- [ ] **Step 6: Add `model_settings` to the `ValidatePayload` allow-list** so it is secret-scanned like `input`.

- [ ] **Step 7: Call the validator from every create path found in Step 2**, resolving the target's catalog from the adapter registry. Validation must run **before** storage, idempotency hashing, and executor enqueue — bad requests must never reach a browser.

- [ ] **Step 8: Run tests**

Run: `cd apps/gateway && go test ./internal/jobcore/... ./internal/httpapi/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add apps/gateway/internal/jobcore apps/gateway/internal/httpapi apps/gateway/internal/grpcapi
git commit -m "feat(gateway): validate model_settings against adapter model catalog at job creation"
```

### Task B4: Envelope injection — `provider_config` + conversation thread ref

**Files:**
- Modify: `apps/gateway/internal/executor/executor.go`
- Test: `apps/gateway/internal/executor/executor_test.go`

**Interfaces:**
- Consumes: `conversations.Manager` (Task B1), `model_settings` (Task A1).
- Produces: worker envelope carrying `options.provider_config` (the model settings) and a conversation block `{key, thread_ref, on_missing}`.

- [ ] **Step 1: Read `apps/gateway/internal/executor/executor.go` in full** — it is ~109 lines and is the whole envelope contract. Note `parseJobOptions` and how a job becomes an envelope. Confirm the envelope's options map is what the worker's `_resolve_provider_config(provider_id, options)` reads; if the envelope uses a typed options struct rather than a passthrough map, add `ProviderConfig map[string]any \`json:"provider_config,omitempty"\`` to it.

- [ ] **Step 2: Write the failing test** asserting the passthrough contract — this is the seam that lets the worker stay unchanged:

```go
func TestEnvelopeCopiesModelSettingsIntoProviderConfig(t *testing.T) {
	// model_settings is the public, catalog-validated contract; provider_config
	// is the internal worker protocol. They are the same flat shape by
	// construction, so this is a copy, not a translation.
	settings := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	got := providerConfigFromModelSettings(settings)
	want := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider_config = %#v, want %#v", got, want)
	}
}

func TestEnvelopeOmitsProviderConfigWhenNoModelSettings(t *testing.T) {
	if got := providerConfigFromModelSettings(nil); len(got) != 0 {
		t.Fatalf("provider_config = %#v, want empty so operator defaults apply", got)
	}
}

func TestProviderConfigDropsReservedKeys(t *testing.T) {
	// _enabled / _new_chat are reserved worker control keys that gate whole
	// phases of the interaction. Defense in depth: the schema and the validator
	// already block them, but the envelope must never carry one through.
	got := providerConfigFromModelSettings(map[string]any{"_enabled": false, "model": "mock-fast"})
	if _, ok := got["_enabled"]; ok {
		t.Fatal("reserved key _enabled leaked into provider_config")
	}
	if got["model"] != "mock-fast" {
		t.Fatalf("model = %v, want mock-fast to survive alongside a dropped reserved key", got["model"])
	}
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/executor/... -run TestEnvelope`
Expected: FAIL.

- [ ] **Step 4: Implement `providerConfigFromModelSettings`** — copy the map, dropping any `_`-prefixed key. Wire it into envelope construction so the envelope's `options.provider_config` is populated only when `model_settings` is present.

- [ ] **Step 5: Inject the conversation block.** When conversations are enabled and the job carries `conversation_id`, resolve the key via `conversations.Manager.Resolve` and add to the envelope:

```
conversation: {
  key:        "<job.conversation_id>",
  thread_ref: "<resolved provider_thread_ref, empty when unbound>",
  on_missing: "<job.options.conversation_missing, default fail>"
}
```

When the manager is nil (flag off) or the job has no `conversation_id`, omit the block entirely so the envelope is byte-identical to today.

- [ ] **Step 6: Run tests**

Run: `cd apps/gateway && go test ./internal/executor/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/gateway/internal/executor
git commit -m "feat(gateway): inject provider_config and conversation binding into worker envelope"
```

### Task B5: Ingest `conversation.*` worker events

**Files:**
- Modify: the `WorkerConsumer` file under `apps/gateway/internal/executor/` (find the exact filename)
- Test: the consumer's existing test file

**Interfaces:**
- Consumes: `conversations.Manager`.
- Produces: `conversation.thread_bound` / `.thread_broken` / `.thread_rebound` projected into the store; the events are **intercepted and not appended** to the job event log.

- [ ] **Step 1: Read the `WorkerConsumer` event-type dispatch switch and the `browser.topology_reported` interception** in full. That interception is the exact template: intercept, force the tenant from the job (never trust the worker's), redact, project, then `continue` so the telemetry event is not appended to the job's lifecycle event log.

- [ ] **Step 2: Write the failing test:**

```go
func TestWorkerConsumerProjectsThreadBound(t *testing.T) {
	// A bound thread ref must land in the conversations store, tenant-forced
	// from the job, and must NOT be appended to the job's lifecycle events.
	// ... construct consumer with a memory conversations store, feed a
	// conversation.thread_bound event, assert Resolve() returns the ref and the
	// job event log length is unchanged.
}

func TestWorkerConsumerThreadBoundIsIdempotent(t *testing.T) {
	// engine.py retries an interaction up to 3x; two identical thread_bound
	// events must leave exactly one row.
}

func TestWorkerConsumerIgnoresConversationEventsWhenDisabled(t *testing.T) {
	// nil manager (flag off) must not panic and must not alter behavior.
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/executor/... -run TestWorkerConsumer`
Expected: FAIL.

- [ ] **Step 4: Implement the interception.** Add the three event names to the switch:
  - `conversation.thread_bound` / `conversation.thread_rebound` → `Manager.Bind` (upsert; idempotent by construction).
  - `conversation.thread_broken` → `Manager.MarkBroken`.
  - **Redaction rule:** accept only the thread URL from the event payload. Never persist any other field. Force `TenantID`/`AppID` from the job record, not the event.
  - Nil manager → skip silently (flag off).

- [ ] **Step 5: Run tests**

Run: `cd apps/gateway && go test ./internal/executor/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/gateway/internal/executor
git commit -m "feat(gateway): project conversation.* worker events into the conversations store"
```

### Task B6: `GET /v1/conversations` + serve wiring behind `UBAG_CONVERSATIONS_ENABLED`

**Files:**
- Modify: `apps/gateway/internal/httpapi/server.go`
- Modify: `apps/gateway/internal/serve/serve.go`
- Test: `apps/gateway/internal/httpapi/server_test.go`

**Interfaces:**
- Consumes: `conversations.Manager`.
- Produces: `GET /v1/conversations` (`job:read`, paginated, tenant/app-scoped, 501 when nil); `httpapi.Config.Conversations *conversations.Manager`.

- [ ] **Step 1: Read the `/v1/alerts` route registration, its paginated list handler, the nil-safe 501 pattern, and `newEnterpriseStoresFromEnv` + the `envBool` usage for `UBAG_RATE_LIMIT_ENABLED` / `UBAG_CACHE_ENABLED`.** Mirror all of them.

- [ ] **Step 2: Write the failing tests:**

```go
func TestConversationsListReturns501WhenDisabled(t *testing.T) {
	// Config.Conversations == nil must yield 501, never a panic.
}

func TestConversationsListIsTenantScoped(t *testing.T) {
	// A caller from tenant B must not see tenant A's conversations.
}

func TestConversationsListRequiresJobRead(t *testing.T) {
	// A principal lacking job:read gets 403.
}

func TestConversationsListPaginates(t *testing.T) {
	// Mirror the /v1/alerts pagination assertions exactly.
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/gateway && go test ./internal/httpapi/... -run TestConversations`
Expected: FAIL.

- [ ] **Step 4: Add `Conversations *conversations.Manager` to `httpapi.Config`**, register `GET /v1/conversations` with `job:read`, and implement the handler by copying the alerts list handler's pagination/scoping shape. Return the typed `ConversationListResponse` from Task A5.

- [ ] **Step 5: Wire `serve.go`.** In `newEnterpriseStoresFromEnv`, gate on `envBool("UBAG_CONVERSATIONS_ENABLED")` (default false). When enabled, construct the store from the existing `storeKind` (`memory` → `NewMemoryStore`, `sqlite` → `NewSQLiteStore(db)`, `postgres` → `NewPostgresStore(db)`) exactly as alerts does, wrap in `NewManager`, and set `Config.Conversations`. When disabled, leave it nil.

- [ ] **Step 6: Run tests**

Run: `cd apps/gateway && go test ./internal/httpapi/... && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/gateway/internal/httpapi apps/gateway/internal/serve
git commit -m "feat(gateway): GET /v1/conversations behind UBAG_CONVERSATIONS_ENABLED"
```

---

## Phase C — Worker

> **Note:** there is deliberately **no model-settings task here.** The gateway flattens `model_settings` into `options.provider_config`, which `_resolve_provider_config` + `ensure_provider_config` already honor. Phase C is conversation affinity, hardening, and the mock adapter.

### Task C1: Conversation event names + value sanity guard

**Files:**
- Modify: `apps/worker/ubag_worker/live/events.py`
- Modify: `apps/worker/ubag_worker/live/engine.py`
- Test: `apps/worker/tests/test_live_adapters.py` (or the closest existing engine test file)

**Interfaces:**
- Produces: event names `conversation.thread_bound`, `conversation.thread_broken`, `conversation.thread_rebound`.
- Produces: `_sanitize_provider_config_value(value: object) -> object` rejecting selector-breaking characters.

- [ ] **Step 1: Read `apps/worker/ubag_worker/live/events.py` in full** and add the three event names following its existing declaration style.

- [ ] **Step 2: Write the failing test** for the sanity guard. The gateway validates against the catalog, but defense in depth matters here because `desired` is interpolated into a selector via `.format(value=desired)`:

```python
def test_provider_config_value_with_selector_metacharacters_is_rejected():
    from ubag_worker.live.engine import _sanitize_provider_config_value
    import pytest
    for bad in ['bad"value', "bad'value", "bad)value", "bad\\value"]:
        with pytest.raises(ValueError):
            _sanitize_provider_config_value(bad)


def test_provider_config_value_plain_label_is_allowed():
    from ubag_worker.live.engine import _sanitize_provider_config_value
    assert _sanitize_provider_config_value("2.5 Pro") == "2.5 Pro"
    assert _sanitize_provider_config_value(True) is True
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/worker && python -m pytest tests/test_live_adapters.py -k provider_config_value -v`
Expected: FAIL (ImportError).

- [ ] **Step 4: Implement `_sanitize_provider_config_value`** and apply it to every value in the dict returned by `_resolve_provider_config`. Booleans pass through untouched (toggle settings); strings containing `"`, `'`, `)`, `\`, or newlines raise `ValueError`.

- [ ] **Step 5: Run to verify**

Run: `cd apps/worker && python -m pytest tests/test_live_adapters.py -k provider_config_value -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/worker/ubag_worker/live/events.py apps/worker/ubag_worker/live/engine.py apps/worker/tests
git commit -m "feat(worker): conversation event names and provider_config value guard"
```

### Task C2: Conversation resume / bind / restart in the live engine

**Files:**
- Modify: `apps/worker/ubag_worker/live/engine.py`
- Modify: `apps/worker/ubag_worker/live/page_driver.py`
- Test: `apps/worker/tests/test_live_adapters.py`

**Interfaces:**
- Consumes: envelope `conversation: {key, thread_ref, on_missing}` from Task B4.
- Produces: `PageDriver.current_thread_url(selectors) -> str` (base returns `""`; `MockPageDriver` returns a settable fake; `PlaywrightPageDriver` returns `self._page.url`).
- Produces: `PageDriver.resume_thread(selectors, thread_ref) -> bool` (base returns `False`).
- Produces: engine emits `conversation.thread_bound` / `.thread_broken` / `.thread_rebound`.

- [ ] **Step 1: Read `engine.py` `_run_interaction` in full** (the `session.new_chat` / `session.configured` block and the surrounding retry loop). Note that `job.new_chat_enabled` already gates `start_new_chat`, and that the interaction retries up to 3×.

- [ ] **Step 2: Write the failing tests** using `MockPageDriver` (the established fake idiom):

```python
def test_resume_navigates_to_bound_thread_and_does_not_start_new_chat():
    # A job carrying conversation.thread_ref must resume that chat, so the
    # end user keeps their context, and must NOT click New chat.
    ...

def test_new_conversation_emits_thread_bound_with_chat_url():
    # First job for an unseen key: after the response, the canonical chat URL
    # is captured and emitted as conversation.thread_bound.
    ...

def test_missing_thread_with_on_missing_fail_raises_conversation_not_found():
    # Default posture: fail loudly with the stable code, mark the binding broken.
    ...

def test_missing_thread_with_on_missing_restart_opens_fresh_chat_and_rebinds():
    # Opt-in self-healing: new chat + conversation.thread_rebound.
    ...

def test_thread_bound_payload_contains_only_the_url():
    # Safe mode: never emit cookies, storage state, or noVNC URLs.
    ...
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/worker && python -m pytest tests/test_live_adapters.py -k conversation -v`
Expected: FAIL.

- [ ] **Step 4: Add `current_thread_url` and `resume_thread` to all three drivers** in `page_driver.py` (`PageDriver` base no-ops, `MockPageDriver` fakes, `PlaywrightPageDriver` real). `resume_thread` navigates to the ref and verifies it loaded (URL settled + `response_container_present(selectors)`), returning `False` when it cannot confirm. Follow `start_new_chat`'s posture: concrete, not abstract; best-effort; never raise `DriftDetectedError`.

- [ ] **Step 5: Implement the engine flow** in `_run_interaction`, before the existing `start_new_chat` block:
  - `thread_ref` present → `resume_thread`. Success → skip `start_new_chat`. Failure → branch on `on_missing`: `fail` → emit `conversation.thread_broken`, raise the error carrying `UBAG-TARGET-CONVERSATION-NOT-FOUND-001`; `restart` → `start_new_chat`, and after the response emit `conversation.thread_rebound` with the new URL.
  - conversation `key` present but no `thread_ref` → normal new chat; after the response, capture `current_thread_url` and emit `conversation.thread_bound`.
  - no conversation block → today's path exactly.

- [ ] **Step 6: Run tests**

Run: `cd apps/worker && python -m pytest tests/test_live_adapters.py -v`
Expected: PASS (including all pre-existing tests — the no-conversation path must stay byte-identical).

- [ ] **Step 7: Commit**

```bash
git add apps/worker/ubag_worker/live apps/worker/tests
git commit -m "feat(worker): resume, bind, and restart provider chat threads by conversation key"
```

### Task C3: FIFO serialization per conversation key

**Files:**
- Modify: `apps/worker/ubag_worker/orchestration/scheduler.py`
- Test: `apps/worker/tests/test_orchestration_scheduler.py`

**Interfaces:**
- Consumes: envelope `conversation.key`.
- Produces: jobs sharing `(tenant, app, target, conversation_key)` execute strictly FIFO; distinct conversations stay parallel under existing AIMD caps.

- [ ] **Step 1: Read `scheduler.py` in full** plus `channel_pool.py` to see how a channel key is computed today, and read `test_orchestration_scheduler.py` for the fake idiom.

- [ ] **Step 2: Write the failing test:**

```python
def test_same_conversation_key_runs_fifo():
    # Two jobs on one conversation must never interleave: typing into the same
    # chat concurrently would corrupt both. Submission order is preserved.
    ...

def test_distinct_conversation_keys_run_in_parallel():
    # Serialization must be per-conversation, not global — otherwise throughput
    # collapses to one job per provider.
    ...

def test_jobs_without_conversation_key_are_unaffected():
    # Flag-off / no-key path keeps today's scheduling exactly.
    ...
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/worker && python -m pytest tests/test_orchestration_scheduler.py -k conversation -v`
Expected: FAIL.

- [ ] **Step 4: Implement per-conversation FIFO** by extending the existing keying concept rather than adding a parallel mechanism. Jobs with no conversation key must take the current code path unchanged.

- [ ] **Step 5: Run tests**

Run: `cd apps/worker && python -m pytest tests/test_orchestration_scheduler.py -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/worker/ubag_worker/orchestration apps/worker/tests
git commit -m "feat(worker): serialize same-conversation jobs FIFO"
```

### Task C4: Mock adapter honors model settings + conversation binding

**Files:**
- Modify: `adapters/mock/ubag_mock_adapter/adapter.py`
- Test: `adapters/mock/tests/test_adapter.py`

**Interfaces:**
- Consumes: payload `job.model_settings`, `job.conversation_id`, envelope `options.provider_config`.
- Produces: deterministic `conversation.thread_bound` event + model settings echoed in the completed event, making the whole path CI-testable without a browser.

- [ ] **Step 1: Read `adapters/mock/ubag_mock_adapter/adapter.py` in full.** This is the registry-dispatched mock (`run(payload)` → `list(iter_events(payload))`); the one under `apps/worker/ubag_worker/adapters/mock/` is a different, unreachable class — do not touch it. Preserve determinism: `_BASE_CLOCK = datetime(2026, 1, 1)`, ids via sha256, no clock/randomness/network.

- [ ] **Step 2: Write the failing test:**

```python
def test_mock_emits_thread_bound_for_new_conversation():
    # Deterministic URL derived from the conversation key via sha256.
    ...

def test_mock_resumes_bound_thread_without_rebinding():
    # A payload carrying a thread_ref must not emit thread_bound again.
    ...

def test_mock_echoes_model_settings_in_completed_event():
    # Lets conformance assert the full model_settings path with no browser.
    ...
```

- [ ] **Step 3: Run and confirm failure**

Run: `cd apps/worker && python -m pytest ../../adapters/mock/tests/test_adapter.py -v`
Expected: FAIL. (Verify the correct invocation path for these tests first — check how `tools/run-python-worker-tests.mjs` invokes them.)

- [ ] **Step 4: Implement.** Derive the fake chat URL deterministically from the conversation key (sha256, matching the existing id idiom). Keep `_contains_disallowed_secret_material` rejection intact.

- [ ] **Step 5: Run tests**

Run: the command from Step 3.
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add adapters/mock
git commit -m "feat(mock-adapter): honor model_settings and conversation binding deterministically"
```

---

## Phase D — SDKs, CLI, dashboard

### Task D1: TypeScript + Go SDK types

**Files:**
- Modify: `packages/sdk-typescript/src/types.ts`
- Modify: the Go SDK job-request struct file under `packages/sdk-go/`
- Test: existing SDK test files

- [ ] **Step 1: Read `packages/sdk-typescript/src/types.ts` (~L25-63)** and the Go SDK's job request struct. Match their existing style (TS uses `"a" | "b" | (string & {})` unions).

- [ ] **Step 2: Add types.** TypeScript:

```ts
export type UbagConversationMissing = "fail" | "restart" | (string & {});

/**
 * Per-job provider UI settings, keyed by the target adapter's own setting keys.
 * Discover the available keys and values from the adapter's model_catalog via
 * the adapters endpoint — they differ per provider (gemini_web: model,
 * thinking; deepseek_web: mode, deepthink).
 */
export type UbagModelSettings = Record<string, string | boolean>;
```

Add `model_settings?: UbagModelSettings | null` to the job type and `conversation_missing?: UbagConversationMissing` to the options type. Mirror both in the Go SDK: `ModelSettings map[string]any \`json:"model_settings,omitempty"\`` on the job struct and `ConversationMissing string \`json:"conversation_missing,omitempty"\`` on the options struct.

- [ ] **Step 2a (Task D1a — conversations SDK method + conformance closure):** add a `listConversations(params?, options?)` method to the TS SDK client and its Go equivalent (`ListConversations`), returning the `ConversationListResponse` shape. Then add the deferred conformance pieces from Task A6 Step 3: a `conversations.list.ok` scenario in `packages/conformance/fixtures/v0/scenarios.json`, its id in `requiredEndpointIds` (`validate-fixtures.mjs`), and the dispatch line in **both** `invokeScenario` (TS, `packages/sdk-typescript/test/conformance.test.mjs`) and the Go conformance harness. Model the method and dispatch on `listAlerts` / `alerts.list.ok` exactly. These land together so `pnpm test:sdk` stays green.

- [ ] **Step 3: Regenerate manifests and check freshness**

Run: `cmd /c pnpm generate:sdk-contracts` then `cmd /c pnpm check:sdk-freshness`
Expected: exit 0.

- [ ] **Step 4: Run SDK tests**

Run: `cmd /c pnpm test:sdk`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/sdk-typescript packages/sdk-go
git commit -m "feat(sdk): model_settings and conversation_missing in TS and Go SDKs"
```

### Task D2: CLI flags

**Files:**
- Modify: the CLI job-create command under `packages/cli/`
- Test: the CLI's existing test file

- [ ] **Step 1: Read the CLI create command's flag parsing** and follow its exact idiom.

- [ ] **Step 2: Write the failing test** asserting `--model`, `--thinking`, `--conversation`, and `--conversation-missing` map onto `job.model_settings.{model,thinking}`, `job.conversation_id`, and `job.options.conversation_missing`, and that omitting them leaves the fields absent (not empty objects).

- [ ] **Step 3: Run and confirm failure**

Run: `cmd /c pnpm test:cli`
Expected: FAIL.

- [ ] **Step 4: Implement the flags.**

- [ ] **Step 5: Run tests**

Run: `cmd /c pnpm test:cli`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/cli
git commit -m "feat(cli): --model, --thinking, --conversation flags on job create"
```

### Task D3: Dashboard conversations list page

**Files:**
- Create: `apps/dashboard/src/routes/conversations/+page.svelte` (+ loader, matching the alerts page structure)
- Test: the dashboard's existing page test file

- [ ] **Step 1: Read `design.md` first** (project rule for any UI edit) plus the existing alerts/jobs list page. Match the NAJM theme DNA: warm cream paper, terracotta accent, editorial rhythm. Strict CSP — no third-party font calls. Do not invent metrics or fixtures.

- [ ] **Step 2: Write the failing test** covering the empty state, the populated state, and the disabled (501) state — the dashboard test suite already asserts accessible state fixtures, so follow that pattern.

- [ ] **Step 3: Run and confirm failure**

Run: `cmd /c pnpm --filter @ubag/dashboard test`
Expected: FAIL.

- [ ] **Step 4: Implement the page.** Consume `GET /v1/conversations` and render `conversation_key`, `target`, `state`, `last_used_at`, `last_job_id`. Show an honest "conversations are not enabled on this gateway" state on 501 — do not fabricate rows.

- [ ] **Step 5: Run tests**

Run: `cmd /c pnpm --filter @ubag/dashboard check && cmd /c pnpm --filter @ubag/dashboard test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/dashboard
git commit -m "feat(dashboard): read-only conversations list page"
```

---

## Phase E — Ledger

### Task E1: Update PROGRESS.md and AGENT_HANDOFF.md

**Files:**
- Modify: `PROGRESS.md`
- Modify: `AGENT_HANDOFF.md`

- [ ] **Step 1: Add a `PROGRESS.md` status-table row and a dated section** describing: new contract fields, `model_catalog`, the 4 error codes, the conversations store + migration 0010, `UBAG_CONVERSATIONS_ENABLED` (default false), `GET /v1/conversations`, worker conversation flows, FIFO, and mock support. State honestly that live-provider verification is manual and not CI-covered.

- [ ] **Step 2: Update `AGENT_HANDOFF.md`** with the new env var, the new route, the migration, and the exact next coding queue (slice 2: provider expansion). **Translate stale `D:\Projects\UBAG` paths to `E:\Projects\UBAG`** if you touch those lines — never propagate them.

- [ ] **Step 3: Commit and push**

```bash
git add PROGRESS.md AGENT_HANDOFF.md
git commit -m "docs: record orchestration-semantics slice in the progress ledger"
git push
```

## Verification

**During implementation (targeted only):**
- `cmd /c pnpm lint:schemas`, `cmd /c pnpm lint:openapi`, `node tools/check-contracts.mjs`
- `cd apps/gateway && go test ./internal/conversations/... ./internal/executor/... ./internal/httpapi/... && go vet ./...`
- `cd apps/worker && python -m pytest tests/test_live_adapters.py tests/test_orchestration_scheduler.py -v`
- `cmd /c pnpm test:adapter-registry`

**End-to-end (CI-safe, mock target, flag on):**
1. Start the gateway with `UBAG_CONVERSATIONS_ENABLED=true`.
2. `POST /v1/jobs` — target `mock`, `model_settings: {model: "mock-deep", thinking: "extended"}`, `conversation_id: "c1"` → accepted; completed event echoes the settings; `conversation.thread_bound` projected.
3. `GET /v1/conversations` → exactly one row for `c1`, `state=active`, `provider_thread_ref` populated.
4. Repeat step 2 with the same key → the row's `provider_thread_ref` is **unchanged** and there is still exactly **one** row (upsert, not append).
5. `POST /v1/jobs` with `model_settings: {model: "does-not-exist"}` → `400` with `UBAG-VALIDATION-MODEL-UNAVAILABLE-001`, and no job row created.
6. Restart the gateway with the flag **off** → `GET /v1/conversations` returns `501`; a job with `conversation_id` behaves exactly as before the change.

**Operator-run full gates (not during coding):** `cmd /c pnpm check`, `cmd /c pnpm test:v0:local`. Live-provider verification (Gemini/DeepSeek model pickers, real chat resume) happens manually in production per the existing operating model — it is ToS-bound and cannot be CI-validated.
