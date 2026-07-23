# UBAG Progress Ledger

Last updated: 2026-07-23

## 2026-07-23 Gateway attachment hardening

Review fixes completed: multipart now invokes the exact shared normal-create preparation path (API version, one-time template application, authorization, kill switch, payload/model/attachment validation, and plugin hooks) before staging; runtime manifest bounds now mirror schema `additionalProperties`, 512-code-point key, and 128-code-point content type limits; chunked multipart uses an adjustable policy-derived stream cap with an explicit 8 KiB framing allowance; and stored-success counters move only after the full multipart artifact set commits. Focused review tests: parser 15 passed, review HTTP 5 passed; combined focused regression 22 parser + 36 HTTP passed; targeted vet/diff-check clean.

Completed and focused-verified the gateway attachment correctness pass: typed manifest errors and filename bounds; legacy audio MIME gating; fail-closed held PUT/multipart MIME, key, per-file and policy-total caps; multipart preflight, streaming SHA-256 and byte-sensitive idempotency; batch held-gate semantics; six-provider catalog policy; labeled metrics; post-dispatch declared-byte immutability with exact replay; SQLite conditional-update CAS; surfaced artifact-list finalize failures; safe materialized filenames/MIME suffixes; and idempotent outbox recovery for queued attachment jobs.

Focused results: attachment parser 19 passed, executor materialization 8 passed, SQLite CAS 2 passed, gateway attachment/catalog 17 passed; targeted `go vet` clean; changed JSON contracts parsed; `git diff --check` clean. No broad suite was run per project instruction.

## 2026-07-23 Multi-file attachments (documents / audio / voice / images / video) + faster pipeline

Generalized the previously audio-only, single-file, undocumented attachment path
into first-class multi-file attachments end-to-end (branch `feat/multi-file-attachments`).

- **Contracts (first):** `job-request` gains an `input.attachments` manifest
  (`{key, filename?, content_type, kind}`); `adapter-manifest` gains an
  `attachments` policy block (`max_files`, `max_file_bytes`, `accepted` per kind —
  the model_catalog analog for files); `errors.json` adds
  `UBAG-VALIDATION-ATTACHMENT(S)-*` and `-MULTIPART-*` codes; OpenAPI adds a
  `multipart/form-data` one-shot `POST /v1/jobs` (+ 413) and documents both flows;
  artifact `type` enum gains `attachment`. New conformance fixtures. SDK contract
  manifests regenerated. `lint:openapi`, `lint:schemas`, `check:contracts`,
  `check:blueprint` pass.
- **Gateway (Go):** new `internal/attachments.DeclaredAttachments` is the single
  source of truth for the declared key set (folds `audio_artifact_key`).
  `materializeAudioArtifact` → `materializeAttachments` (N files, ordered
  `attachment_local_paths`, fail-closed, partial-failure cleanup) wired into both
  process and daemon runners. Jobs that declare attachments are held in
  `StatusCreated` and enqueued exactly once — via a new `TransitionStatus` CAS
  primitive (memory/sqlite/postgres) — only after every declared artifact key is
  uploaded (PUT completion hook on fresh + replay branches) or immediately for the
  multipart one-shot (staged temp files + rollback). A TTL sweeper fails jobs that
  never receive their uploads. Per-adapter content-type validation (BOM-tolerant
  manifest loader); `safeArtifactContentType` allowlist reconciled with the
  adapter policies. New attachment metrics.
- **Worker (Python):** the live engine reads `input.attachments` +
  gateway-injected `attachment_local_paths` and attaches every file in one
  `driver.attach_files` call, emitting `file.attached` with the declared keys;
  `audio_artifact_key`/`audio_local_path` keep working as the single-audio alias.
  Fixed a latent bug: the gateway now intercepts the `file.attached` worker event
  as telemetry so its non-lifecycle type can never fail a job.
- **Adapters:** `file_attach` capability + `attachments` policy on all 6 web
  providers (activated Claude's dormant `file_upload_later`).
- **SDKs + CLI:** `submitJobWithAttachments` (key-reference + parallel uploads) and
  `createJobMultipart` (one-shot) in both TypeScript and Go SDKs; CLI
  `create-job --attach <comma-separated paths>` (multipart, content-type + kind
  inferred from extension).
- **Verification run:** gateway `go vet` + `internal/{attachments,jobs,executor,httpapi}`
  tests (incl. new gate/multipart/materialize tests); worker 212 tests (incl. new
  `test_attachments.py`); TS SDK typecheck + conformance (49); Go SDK build/vet/tests;
  CLI build + tests. Full local gate (`pnpm test:v0:local` / `pnpm check`) is the
  user's to run.
- **Live DOM verification (all 3 target providers):** inspected the logged-in
  ChatGPT / DeepSeek / Gemini composers read-only via the Chrome extension.
  ChatGPT (`input[type='file'][multiple]` at rest) and DeepSeek (hidden
  `input[type='file']` after load) match their `file_input` baselines — attach
  works unchanged. Gemini renders **no** file input until "Upload & tools" →
  "Upload files" fires the native chooser, so the worker gained a
  `file_attach_trigger` click-path (verified selectors) and a Playwright
  `expect_file_chooser` interception path in the driver, covered by mock tests
  (215 worker tests green); the Playwright path still needs one live Gemini worker
  run to confirm the real chooser.
- **BOM regression fixed:** removed the UTF-8 BOM from all eight adapter
  manifests and hardened `loadModelCatalogFromDisk` through a BOM-tolerant
  decoder. `TestDecodeModelCatalogAcceptsUTF8BOM` exercises genuinely
  BOM-prefixed bytes so the defensive behavior cannot regress silently.

## 2026-07-23 Full tracked-file parity (local ↔ GitHub ↔ VPS)

- Verified local `main` is level with `origin/main` at `755772a` (0 ahead / 0 behind); the only untracked item is `.serena/` (local LSP cache), so local ↔ GitHub is already exact.
- Audited production `/opt/docker/ubag` against every GitHub-tracked file using canonical git blob hashes (`git ls-tree -r HEAD` vs `git hash-object` on the VPS) to avoid false CRLF mismatches on the Windows checkout. Result before sync: 1,121 of 1,336 tracked files matched byte-for-byte, 0 mismatches, and 215 files missing — all of them the `.codex/skills/hallmark/` design skill excluded by the prior `1,121`-file sync.
- Shipped only the 215 missing tracked files via `git archive HEAD -- .codex/skills/hallmark` (canonical LF, tracked-only) and extracted into `/opt/docker/ubag`. Purely additive: no overwrites, no deletions, and no `deploy/vps/env.local`, `.htpasswd`, databases, logs, or runtime artifacts read or touched. No image rebuild (`.codex/` is not part of any container image), so all containers stayed up: `ubag-vps-gateway-1`, `ubag-nginx-dashboard`, `ubag-vps-chat-reaper`, `ubag-vps-browser` remained healthy.
- Post-sync re-audit over all 1,336 tracked files: `MISSING=0  MISMATCH=0  EXTRA_TRACKED=0`. Production now mirrors GitHub tracked source exactly.

## 2026-07-23 Gemini 3.6 Flash Standard + three-way source sync

- Rebased the local checkout from `6178968` to GitHub `origin/main` at `9da31f5` (109 commits) while retaining the pre-sync dirty tree in `stash@{0}` as a recovery copy.
- Compared the production source tree at `/opt/docker/ubag` against a fresh GitHub clone without reading or copying `deploy/vps/env.local`, `.htpasswd`, runtime databases, logs, spool payloads, or generated binaries. Production's worker engine/page driver match GitHub; older production gateway/PAT/dependency copies were intentionally not promoted over newer GitHub code.
- Updated Gemini's native `ProviderSetting` policy from `3.5 Flash` + Extended to `3.6 Flash` + an idempotent `Extended thinking = off` toggle. Google stores model and thinking independently, so selecting `3.6 Flash` alone does not guarantee Standard thinking.
- Production live-DOM verification confirmed the selected menu state contains only `3.6 Flash`, while the picker reads `Flash` rather than `Flash Extended`.
- Rebuilt and recreated `ubag-vps-gateway-1`; container health returned `healthy` on the new image. Production smoke job `job_000000000028` completed with selector version `2026-07-23-gemini-3.6-standard` and exact output `UBAG_GEMINI_36_STANDARD_OK`.
- The only production-only source promoted into the shared codebase is the verified Gemini selector policy. Server-only secrets and runtime artifacts remain untracked.
- Post-merge validation passed: `cmd /c pnpm test:worker` (208 tests plus JSONL smoke), dashboard `svelte-check` (0 errors/warnings), dashboard Vitest (17 tests), `cmd /c pnpm test:deployment`, `cmd /c pnpm test:docs` including responsive widths, and `git diff --check`.
- Pushed shared commit `acec1ed` to GitHub `main`, then synchronized all 1,121 GitHub-tracked files into `/opt/docker/ubag`; a hash audit reported zero missing files and zero mismatches. The prior production source and dashboard artifact are recoverable under `/opt/docker/ubag-sync-backups`.
- Rebuilt the exact synchronized gateway image (`sha256:dde174b3d9422bba95c4022c753171f7b0ae830a3617acec6a798875eec52559`) and recreated gateway, chat-reaper, and nginx-dashboard. Gateway and nginx-dashboard are healthy; the existing browser remains healthy.
- Final post-sync production smoke `job_000000000029` completed with exact output `UBAG_SYNCED_GEMINI_36_STANDARD_OK` and selector version `2026-07-23-gemini-3.6-standard`.

## 2026-07-17 PAT (Personal Access Tokens) wired into serve + made persistent

Companion to the App JWT wiring below. The gateway's PAT layer (`internal/pat`,
`POST /v1/auth/pat`, `ubag_pat_…` bearer auth) was implemented but unreachable
in production — `httpapi.Config.PAT`/`PATDefaultTTL` were never set from env — and
the `pat` package had **only** an in-memory store, so issued tokens would vanish
on every restart (useless for a long-lived credential). Two gaps closed:

- **Persistent stores (`pat/sqlite.go`, `pat/postgres.go`)** mirroring `session`:
  only the SHA-256 hash of each token is persisted (a store leak reveals no
  usable credential); `expires_at` NULL = non-expiring; revocation is a soft
  flag. SQLite self-bootstraps its DDL in `Ready()`; Postgres is migration-driven
  (`migrations/postgres/0011_personal_access_tokens.sql`, auto-applied by the
  glob-based runner) and `Ready()` asserts the table via `to_regclass`. This
  mirrors the conversations precedent (Postgres-only migration + SQLite
  self-bootstrap; no `migrations/sqlite` counterpart needed).
- **Env wiring (`serve.go`)** — `UBAG_PAT_ENABLED` gates the whole feature
  (default off ⇒ route stays 501; opt-in because it mints credentials, matching
  the App JWT philosophy). When on, the store follows the gateway store kind
  (memory/sqlite/postgres) so tokens survive restarts on sqlite/postgres.
  `UBAG_PAT_DEFAULT_TTL_MS` sets the default issued-token TTL (positive integer
  ms; unset ⇒ no default expiry).
- **Bug fix (`pat_handlers.go`)** — the tenant/app override in `handleIssuePAT`
  only fired for `role == "admin"`, but `auth:pat:issue` is authorized for
  `superadmin` **only**, so *no* role could both issue and scope a PAT — every
  PAT collapsed to the issuer's own tenant, defeating per-client identity. The
  override now also allows `superadmin`. Surfaced by TDD (`TestPATIssueThenAuthenticate`).

Tests (TDD, red first): `pat/sqlite_test.go` (round-trip, expiry, revoke,
unknown, hash-not-raw, persistence across store instances),
`serve/pat_env_test.go` (disabled-by-default, memory when enabled, TTL parse),
`httpapi/pat_auth_test.go` (superadmin issues → PAT authenticates scoped;
per-tenant job isolation + cross-tenant 404; issuance requires superadmin;
disabled ⇒ 501 + unknown PAT 401; revoked ⇒ 401). `go test ./internal/pat/
./internal/serve/ ./internal/httpapi/` + `go vet` green.

Live-verified (local gateway :58080, sqlite, `UBAG_PAT_ENABLED=true`): superadmin
app-secret issued a PAT scoped to `tenant_radiology/radiology-assist`; the token
authenticated, created a job, was invisible to a `tenant_law` PAT (list + 404),
and a `service`-role PAT could not issue another PAT (403). After a full gateway
restart the pre-restart token still authenticated (200) — SQLite persistence
confirmed. Deploy env examples + docs security-model page updated.

Not done (deliberate): no PAT listing/revocation REST endpoints (only issuance
exists today; `Revoke` is store-level), gRPC stays app-secret-only, no
`packages/security` `auth.personal_access_token.*` audit-event contract yet.

## 2026-07-17 App JWT auth wired into serve: per-client (tenant, app) identity

Multi-client readiness audit found the one production gap for serving many
downstream projects (OET, IELTS, radiology, business admin, law, …) from one
deployment: the `internal/appjwt` RS256 layer and `httpapi.Config.AppJWTPublicKey`
existed and were unit-tested, but `serve.go` never loaded a key from env — so a
deployed gateway was app-secret-only and every client collapsed into one shared
`(tenant, app)` scope. Isolation between clients rested entirely on disjoint
conversation-key namespaces, and any client could list every client's jobs.

- **`serve.go`: `appJWTPublicKeyFromEnv()`** — `UBAG_APP_JWT_PUBLIC_KEY` (inline
  PEM; literal `\n` accepted for single-line .env values) or
  `UBAG_APP_JWT_PUBLIC_KEY_FILE` (mounted PEM; inline wins when both set).
  Accepts PKIX ("PUBLIC KEY") and PKCS#1 ("RSA PUBLIC KEY") RSA keys; non-RSA or
  malformed input **fails startup** rather than silently running without JWT
  auth. Both unset ⇒ nil key ⇒ unchanged app-secret-only behavior.
- **withAuth hardening (`httpapi/server.go` `validAppJWTClaims`)** — a correctly
  signed token whose `tid`/`sub`/`role` is empty or not exactly its trimmed
  form no longer authenticates (empty claims previously produced a shared
  `""/""` principal scope, defeating exactly the isolation JWTs exist for, and
  pooling rate-limit buckets; padded claims are rejected rather than normalized
  inside the trust boundary). `exp==0` (never-expiring) is rejected per §11's
  short-lived contract, and accepted lifetime is capped at 24h
  (`maxAppJWTLifetime`) so a leaked long-exp token cannot grant access until
  the shared key is rotated. Rejected tokens fall through to the remaining auth
  branches and surface as the generic 401.
- Client tokens carry `tid` (tenant), `sub` (app id), `role` (case-sensitive;
  `service` is the right role for job-submitting clients), `iat`, `exp`; RS256
  only, minted with `appjwt.IssueToken`. App-secret, PAT, and SSO branches are
  untouched; gRPC remains app-secret-only (documented limitation).

Tests (TDD, red first): `httpapi/appjwt_auth_test.go` — first coverage of the
withAuth JWT branch (per-client job scoping incl. cross-tenant 404 + app-secret
tenant blindness; empty/whitespace-claim rejection; padded-claim rejection;
exp==0 rejection; 48h-exp rejection with 1h accepted; valid JWT against a
JWT-disabled gateway → 401; expired/foreign-signature rejection; app-secret
coexistence) and `serve/appjwt_env_test.go` (unset/inline/escaped-newline/file/
precedence/PKCS#1/malformed/missing-file/non-RSA). `go test ./internal/httpapi/
./internal/serve/ ./internal/appjwt/` + `go vet` green. A 3-lens adversarial
review of the diff (auth-bypass, config-loading, test-validity) found no
blockers; its should-fix (max-TTL cap) and nits (padded-claim normalization,
missing nil-config test) were applied before commit.

Live e2e (local gateway :58080, sqlite + file spool + worker consumer + mock
adapter): 5 clients with distinct JWT identities ran 15 concurrent jobs, all
completed with zero cross-contamination; all five deliberately shared the SAME
conversation key ("consult") and got five isolated per-tenant conversation
rows; per-client job listings fully scoped; cross-tenant GET → 404; the
app-secret principal saw none of it; expired/empty-tid/no-exp/garbage tokens
all 401. Deploy env examples updated (`deploy/small/env.example`,
`deploy/vps/env.example`, `deploy/multi-region/env.example`); docs-site
security model page updated.

Not done (deliberate): no token-issuance endpoint (§11 says JWTs are "derived
from app secret" — adding `/v1/auth/token` is a contract change that must go
through `packages/openapi` first), no key rotation/JWKS (single env key; §11.3
dual-accept grace is a follow-up), no `iat`/`nbf` validation (issuer-controlled;
pre-issued tokens are usable before their intended window), no
`packages/security` app_jwt contract parity (no `auth.app_jwt.*` audit event
names yet), gRPC not extended.

## 2026-07-17 Chat reaper: delete UBAG's own stale job chats (never the human's)

Operator ask: "all chats after 2 hours should be deleted — to avoid cluttering."
Implemented, but NOT as literally asked, because the worker could not delete
chats at all AND the literal rule was unsafe:

- **The accounts are real and shared.** The live ChatGPT sidebar holds 26+ chats
  mixing UBAG's throwaway job chats ("Math Query", "72", "Memory game BANANA")
  with the operator's actual work ("Cloud Code Project Refactor", "RadioPad UI
  Design", "ChatGPT Business Agents"). Provider deletion is PERMANENT — no trash.
  "Delete everything older than 2h" would have destroyed the second list.
- **UBAG could not tell them apart.** `engine.py` captured `current_thread_url`
  only when `job.conversation_key` was set (engine.py:432), so ad-hoc jobs left
  no record; `gateway_conversations` had 0 rows in production. So the ONLY
  implementable literal rule was the destructive one.

Design (operator-confirmed): only ever touch chats UBAG **recorded itself as
creating**, making "we only delete our own" structural rather than a heuristic.

- `ubag_worker/live/chat_ledger.py` — append-only JSONL of chats UBAG created
  (url, conv_id, target, created_at, conversation_key). Best-effort by design:
  a ledger failure must never fail a job that already answered, so every failure
  biases toward UNDER-recording (missed cleanup = clutter; over-recording = data
  loss). This ledger IS the reaper's allowlist.
- `engine.py` gains an optional `chat_sink` (default None ⇒ byte-identical
  behavior, no gateway/contract change needed since nothing new is emitted).
- `page_driver.delete_chat(selectors, conv_id)` + `ChatDeleteFlow` selectors.
  Ids are charset-guarded (`_SAFE_CONV_ID_RE`) before touching a selector — a
  quote/bracket could widen the selector and delete OTHER chats. Returns True
  only when the chat is VERIFIED absent afterwards, never on a click.
- `run_chat_reaper.py` + `chat-reaper` compose service (15-min loop, 0.1cpu).
  Dry-run is the default (`UBAG_CHAT_REAPER_ENABLED`); skips bound threads.
- **ChatGPT delete flow verified live 2026-07-17** by deleting a UBAG-created
  throwaway chat. The row options button carries the id directly
  (`data-conversation-options-trigger="<uuid>"`), enabling exact id-addressed
  deletion — no title/age/position matching is even expressible. Menu exposes
  stable testids (`delete-chat-menu-item` → `delete-conversation-confirm-button`).
  The options button needs `element.click()`: sidebar overlays intercept a
  positional click and would dispatch to the WRONG element.
  gemini_web/deepseek_web have no verified flow yet ⇒ `delete_chat=None` ⇒ the
  reaper refuses them rather than improvising against a real account.

Two real bugs found and fixed while verifying:

1. **The bridge restart loop never ran.** `deploy/vps/browser/entrypoint.sh` runs
   under `set -eu`, which the restart subshell inherits: when node exited
   non-zero, errexit killed the SUBSHELL, so the bridge stayed dead until the
   container was recreated (observed: browser unhealthy for ~1h, 0 restart
   markers). Fixed with `set +e` inside the subshell; verified by killing the
   bridge and watching it return in <8s.
2. **The worker never saw the ledger env.** The gateway passes the worker a
   curated allowlist (`minimalWorkerEnv`, workerconsumer.go) to keep secrets out
   of the worker; the two non-secret ledger vars were missing, so recording
   silently no-op'd. Added them. A second layer: the `chat_ledger` named volume
   initialized root-owned (the image never created that dir, unlike
   executor-spool), so the ubag-uid worker got EPERM and `record_chat` swallowed
   it exactly as designed. Fixed in gateway.Dockerfile.

Verified live end-to-end: job → ledger record → `reaper.deleted` (verified gone)
→ `deleted_at` stamped; targeting dry-run on a mixed ledger reaped 1 of 4 and
correctly skipped bound / too-young / already-deleted; the operator's own chats
remain untouched. 386 worker tests (19 new), go vet clean.

**Follow-up fix (same day):** `delete_chat` treated "options row not found" as
"already gone" and returned success. ChatGPT paints its sidebar seconds after
domcontentloaded, so on the reaper's freshly-opened page the row simply was not
there yet within the 4s probe — the reaper reported `reaper.deleted`, stamped
`deleted_at`, and never retried. Caught on the live account: a chat the reaper
had "verified gone" was still in the sidebar. Failed safe (clutter kept, nothing
wrongly deleted) but it made the reaper lie about its one irreversible action.
Added `ChatDeleteFlow.list_ready` (`a[href^='/c/']`): an absence is trusted only
once the chat list has rendered, else `delete_chat` refuses to conclude and
returns False. Regression test asserts the gate exists and that every
id-addressed template still consumes `{conv_id}`.

**Note:** the 28 chats predating the ledger are unrecorded, so the reaper will
never touch them. The 10 that were UBAG/test artifacts (math probes + the BANANA
multi-turn tests) were deleted manually on operator request after an
id+aria-label match check; sidebar went 28 → 18 with every operator chat intact.
The 3 "Pong Request" chats were deliberately left — not provably UBAG's.
Automatic cleanup applies only to chats created from now on.

## 2026-07-17 gemini_web: re-baseline the flattened mode menu (3.5 Flash + Extended)

The operator's gemini/deepseek defaults were ALREADY the requested values
(gemini `3.5 Flash` + `Extended`; deepseek `Expert` + `deepthink` on, all
`required=True`), so no default changed. But verifying them against the live DOM
surfaced real drift in Gemini:

- **Google flattened the mode picker.** The nested `Thinking level` gem-menu-item
  (submenu: Standard / Extended) is GONE. The single menu behind
  `data-test-id='bard-mode-menu-button'` now lists
  `3.1 Flash-Lite | 3.5 Flash | 3.1 Pro | Extended thinking`, and the label
  "Standard" no longer exists anywhere.
- The old second open_step (`gem-menu-item:has-text('Thinking level')`) matched
  nothing; `_open_control` silently broke out of it and the setting still
  resolved off the top-level menu — so jobs kept passing while burning a 4s click
  timeout each. Dropped it; `satisfied_when`/`apply_click` were already correct
  for the flat list.
- **Verified live that model and Extended thinking are NOT mutually exclusive:**
  clicking "Extended thinking" leaves "3.5 Flash" selected (both carry
  `.selected`), so "3.5 Flash WITH extended thinking" is achievable and two
  independent `choice` settings still model it correctly. `satisfied_when` gates
  the click, so an already-on Extended is never clicked again (which would toggle
  it back OFF).
- `adapters/gemini_web/manifest.json` model_catalog corrected: dropped
  `"Standard"` (a trap — the gateway would accept it, then the job would fail
  with DriftDetectedError since no such label exists) and added the
  now-proven `3.1 Flash-Lite` / `3.1 Pro` models.
- selector_version `2026-07-15-prompt-input-rebaselined` →
  `2026-07-17-mode-menu-flattened`.

**Enforcement proven directly** by running `run_live_worker.py --input` in
isolation and reading the `session.configured` event data (the gateway
deliberately does not persist that event — see workerconsumer.go:318):

```
gemini_web:   [{key:model,   desired:"3.5 Flash", state:already_set},
               {key:thinking,desired:"Extended",  state:set}]        -> "9"
deepseek_web: [{key:mode,    desired:"Expert",    state:set},
               {key:deepthink,desired:true,       state:already_set}] -> "16"
```

`state:set` = UBAG actively applied it (it was not already correct). A separate
drift test forced gemini to `3.1 Pro` and the next job restored `3.5 Flash`.
Note gemini/deepseek mode appears to be per-conversation (a fresh page resets to
defaults), so inspecting a NEW page after a job cannot distinguish "enforced"
from "default" — read the worker's event data instead. 367 worker tests pass.

## 2026-07-17 chatgpt_web: pin GPT-5.6 Sol + Medium intelligence

Operator decision change: chatgpt_web previously shipped **no** settings on
purpose (selectors.py comment, 2026-06-29: *"no forced model/mode for ChatGPT
(leave the account default)"*). The operator now requires every ChatGPT job to
run on **GPT-5.6 Sol** at **Medium** intelligence, so chatgpt_web now enforces
both, like gemini_web/deepseek_web already did.

- **DOM re-baselined 2026-07-17 against live chatgpt.com** (required — the old
  `data-testid='model-switcher-dropdown-button'` no longer exists). Both controls
  sit behind ONE composer pill (`button.__composer-pill[aria-haspopup='menu']`)
  whose label is the current intelligence level. Its menu holds the intelligence
  levels as `[role=menuitemradio]` (Instant 5.5 / Medium / High / Pro — Pro is
  `cursor-not-allowed` on this account) plus a nested
  `[role=menuitem][aria-haspopup=menu]` opener (labelled with the CURRENT model)
  that reveals the models, also `[role=menuitemradio]`. Selected = `aria-checked`.
- Matching on `menuitemradio` disambiguates the submenu OPENER (role=menuitem)
  which carries the same "GPT-5.6 Sol" text. Verified on the live DOM that
  `:has-text("Medium")` matches exactly 1 row and no model label contains
  "Medium", so the two settings cannot cross-match.
- `_open_control` **clicks** (not hovers) each open_step — verified clicking the
  nested opener does reveal the model radios (9 radios visible).
- Model is enforced BEFORE thinking (declaration order), since switching model
  can reset the intelligence level. `reasoning=True` so Medium's think isn't
  mistaken for a hang. `required=True` (default): if the pin can't be confirmed
  the job fails loudly rather than silently answering on the wrong model.
- `adapters/chatgpt_web/manifest.json` model_catalog filled in to match the
  proven labels (was `{}`, which made the gateway reject any client-sent
  `model_settings` for ChatGPT). "Pro" deliberately omitted — it is not
  selectable on this account.
- selector_version bumped `2026-06-29-newchat-verified` → `2026-07-17-model-pinned`.
- **Live-verified enforcement (not just observation):** drifted the account to
  thinking=High, ran a job → job completed AND the account was forced back to
  `model='GPT-5.6 Sol' thinking='Medium'` (checked radios: `['Medium']`), with
  `session.configured` in the gateway log. Note `session.new_chat`/
  `session.configured` are deliberately NOT persisted to the job event log
  (workerconsumer.go:318 logs and skips them as informational) — check the
  gateway log, not `/v1/jobs/{id}/events`, to confirm the config phase ran.
- Tests updated to the new intent (they had codified the old "no settings"
  decision): 367 worker tests pass; adapter-registry 21/21; contracts green.

## 2026-07-17 VPS: live-browser (VPS-hosted Chrome) for 24/7 provider sessions

Added server-side live-browser so provider logins live on the VPS and jobs run
24/7 with the operator's laptop off (owner-approved lifting the earlier
1-core/2GB cap to ~2 cores/4GB). The operator signs in once via the dashboard's
Browser Sessions widget — verified live: the ChatGPT page streamed into
`https://ubag.polytronx.com/dashboard/browser` with the widget showing "Live".

- **New `browser` service** (`deploy/vps/browser/{Dockerfile,entrypoint.sh}`):
  headed Google Chrome on Xvfb (reusing the proven browser-viewer flags —
  stealth + `--password-store=basic` + `--no-sandbox`), streamed by
  `tools/live-browser/bridge.mjs` in a new **attach-only** mode. A foreground
  watchdog owns Chrome (relaunch-on-death); the bridge only attaches + streams,
  so it never launches Chrome with the desktop/loopback flags that fail as root
  in a container. Persistent profile on a named volume (`browser_profile`).
  socat exposes Chrome's loopback CDP to the worker on 9223. Capped 1.0cpu/1.9G.
- **bridge.mjs patches (all env-gated, local Windows dev unchanged):**
  `UBAG_LIVE_BROWSER_BIND` (0.0.0.0 in-container), `UBAG_LIVE_BROWSER_ATTACH_ONLY`
  (never spawn Chrome), and process-level `unhandledRejection`/`uncaughtException`
  handlers that re-attach instead of crashing. The last one fixes a real
  shared-Chrome bug: when the live worker opens/closes its own page, an in-flight
  CDP command returned "Not attached to an active page" and killed the bridge —
  now it self-heals, plus a restart-loop supervisor in the entrypoint.
- **Gateway → live worker:** `UBAG_WORKER_SCRIPT=run_live_worker.py` (mock still
  routes to the mock adapter) + `UBAG_REMOTE_BROWSER_ENDPOINT=http://172.28.0.10:9223`.
  Must be an **IP, not a hostname** — Chrome's DevTools HTTP endpoint returns 500
  for any `Host` header that isn't localhost/an IP, so `ubag-private` is pinned to
  `172.28.0.0/24` and the browser holds static `172.28.0.10`.
- **`ubag-private` is no longer `internal`:** the browser needs outbound internet
  to reach providers (an internal net gave Chrome ERR_NAME_NOT_RESOLVED). Only
  app-tier traffic rides it, Postgres is on `platform`, and nothing publishes a
  host port — so this grants egress only, no new inbound exposure.
- **Dashboard + nginx:** `LiveBrowser.svelte` `defaultWsUrl()` now targets
  `wss://<host>/live-ws` off-localhost (localStorage override still wins); nginx
  adds an authed `/live-ws` WebSocket proxy (server-level Basic Auth inherited —
  the bridge grants full Chrome control).
- **Gotcha that cost the most time:** the NPM proxy host for ubag.polytronx.com
  needs **"Websockets Support" enabled** — without it NPM strips the `Upgrade`
  header and the browser WS handshake reaches nginx as a plain GET (426). Enabled
  it; the widget connected immediately. (Internal handshake was 101 the whole
  time — the break was purely the NPM toggle.)
- Verified: live widget streams Chrome; worker attaches over CDP and progresses a
  chatgpt_web job through session/token events; bridge survives worker page churn;
  mock jobs still complete; all 3 containers healthy at ~1.5cpu/0.9G actual use.

## 2026-07-17 VPS: UBAG moved onto the shared-platform Postgres

The production VPS (185.252.233.186) now runs a shared backing-services stack at
`/opt/platform` (one Postgres 17+pgvector, one MinIO, one Redis 7, one Soketi for
every project on the box — authored in the separate `vps-platform` repo at
`E:\Projects\vps-platform`; policy in `/opt/platform/PLATFORM-RULES.md`, exemption:
`oet-*` only). UBAG changes:

- `docker-compose.vps.yml`: own `postgres` service removed; gateway joins the
  external `platform` network and reads `UBAG_POSTGRES_DSN` from
  `deploy/vps/env.local` (credentials generated by
  `/opt/platform/bin/provision-project.sh ubag`). Budget drops to 0.70 cpu/~1.2G.
- Data migrated with row-count parity (24 tables, incl. 9 applied schema
  migrations) via `/opt/platform/bin/migrate-pg.sh`; old `ubag-vps_postgres_data`
  volume retained ≥7 days for rollback; pre-migration dump archived off-host.
- `deploy/small/ci-deploy.sh`: stale paths fixed (`/opt/ubag` →
  `/opt/docker/ubag`, `docker-compose.small.yml` → `docker-compose.vps.yml`,
  container names `ubag-small-*` → `ubag-vps-gateway-1`/`ubag-nginx-dashboard`).
  The forced-command entry in the VPS `authorized_keys` was updated to match.
- Verified live: gateway healthy on platform Postgres (`/v1/ready` all checks
  true via the nginx docker-network path), dashboard `/healthz` OK.
- `docker-compose.small.yml` (local/small profile) is unchanged — it still runs
  its own backing services for self-contained local use.
- Nginx Proxy Manager proxy host added for `ubag.polytronx.com` (Let's Encrypt,
  Force SSL, HTTP/2, Block Common Exploits) forwarding to `ubag-nginx-dashboard`
  over the existing `nginx-proxy-manager_default` docker network — no new host
  port published (this VPS has no active firewall; every 0.0.0.0-bound port is
  directly internet-reachable, so container-network-only ingress was used
  instead). Deployment scope is intentionally API + dashboard with mock/adapter
  jobs only (`UBAG_EXECUTOR_MODE=file` + `UBAG_WORKER_CONSUMER_ENABLED=true` +
  `run_mock_worker.py`, `UBAG_ARTIFACT_STORE=localfs`) — no live-browser
  automation, to fit the 1-core/2GB budget the owner set for this box.
- End-to-end verified through the public domain: `job_000000000003` submitted
  via `POST https://ubag.polytronx.com/v1/jobs` (operator Basic Auth, gateway
  bearer token injected server-side) reached `completed` with real mock output.
- Found and fixed a latent bug while wiring worker-consumer mode into Docker
  for the first time: the `UBAG_WORKER_PYTHON=/usr/bin/python3` default doesn't
  exist in the `python:3.12-slim` gateway image (interpreter lives at
  `/usr/local/bin/python3`) — never hit before because
  `UBAG_WORKER_CONSUMER_ENABLED` defaults to `false` everywhere it appeared.
  Fixed the default in `docker-compose.small.yml`, `deploy/small/env.example`,
  and `deploy/helm/ubag/values.yaml` (same `ubag/gateway` image, same bug).
  Verified on the VPS: `docker build -f deploy/small/gateway.Dockerfile -t
  ubag/gateway:small-local .` then `docker run --rm --entrypoint sh
  ubag/gateway:small-local -c "which python3"` returns `/usr/local/bin/python3`;
  `/usr/bin/python3` doesn't exist in the image.
- Removed leftover artifacts from an earlier (2026-07-11/15) manual live-browser
  probe against this box (`/root/ubag-probe.sh`,
  `/root/ubag-rotated-credentials-20260711.txt`,
  `/root/probe-baseline-gemini_web.json`) — confirmed with the owner as
  expected/historical before deleting.

## 2026-07-16 Orchestration Semantics (per-request model/mode + conversation affinity)

Slice 1 of the AI-orchestrator gap roadmap (see `docs/superpowers/specs/2026-07-15-orchestration-semantics-design.md` and `docs/superpowers/plans/2026-07-15-orchestration-semantics.md`). All new runtime behavior is inert by default behind `UBAG_CONVERSATIONS_ENABLED` (default false); the no-conversation / no-model-settings path is byte-identical to before.

- **Contracts**: `job.model_settings` (flat map keyed by each adapter's own setting keys, values string|boolean — e.g. gemini `{model,thinking}`, deepseek `{mode,deepthink}`) and `job.options.conversation_missing` (`fail`|`restart`) added to `packages/shared-schemas/schemas/job-request.schema.json` and mirrored in OpenAPI. `job.conversation_id` (already present) is now honored as an opaque conversation key scoped to `(tenant, app_id, target)`. Four error codes registered in `errors.json` under existing categories: `UBAG-VALIDATION-MODEL-UNAVAILABLE-001`, `UBAG-VALIDATION-MODE-UNAVAILABLE-001`, `UBAG-TARGET-CONVERSATION-NOT-FOUND-001`, `UBAG-TARGET-CONVERSATION-BROKEN-001`. Adapter manifests gained a `model_catalog` block (`settings: {key: {kind: choice|toggle, values?}}`) validated + surfaced by `packages/adapter-registry`; catalogs ship only labels proven by the current `selectors.py` baseline (gemini model `3.5 Flash`, thinking `Standard`/`Extended`; deepseek mode `Expert`, `deepthink` toggle), `chatgpt_web` empty, `mock` synthetic. New route `GET /v1/conversations` documented. Conformance grew three scenarios (model_settings accepted, out-of-catalog rejected, conversations list); 44 scenarios total.
- **Gateway**: new nil-safe `apps/gateway/internal/conversations` package (memory/SQLite/Postgres, modeled on `internal/alerts`); `Bind` is a true upsert on `(tenant,app,target,key)`; SQLite self-bootstraps DDL in `Ready()`, Postgres asserts via `to_regclass` and ships `migrations/postgres/0010_conversations.sql`. Every job-create path (httpapi `createJob` + `processBatchEntry`, grpcapi `CreateJob`) validates `model_settings` against the target's `model_catalog` before storage/idempotency/enqueue, and `model_settings` is in the payload secret-scan allow-list. Validated `model_settings` is injected into `options.provider_config` at create time (the worker already reads that key), so it persists for retry and flows through every dispatch path; any client-supplied `provider_config` is stripped first (it is a gateway-internal channel — letting a client set it would bypass catalog validation, and the value is interpolated into a Playwright selector). `WorkerConsumer` projects `conversation.thread_bound/_broken/_rebound` events (tenant forced from the job, chat-URL only, intercepted not appended to the job event log) and injects the conversation block into the envelope at dispatch-to-worker time. `GET /v1/conversations` (`job:read`, paginated, nil-safe 501) surfaces bindings.
- **Worker**: `engine.py` reads the envelope `conversation {key, thread_ref, on_missing}` block — resume via `page_driver.resume_thread` (skip new chat), or on-missing `fail` → raise `UBAG-TARGET-CONVERSATION-NOT-FOUND-001` + emit `thread_broken`, `restart` → fresh chat + `thread_rebound`; a key with no ref → new chat then capture `current_thread_url` and emit `thread_bound`. Events carry only the chat URL (safe mode). `scheduler.py` serializes same-`(provider, conversation_id)` jobs strictly FIFO while distinct conversations stay parallel under AIMD. A `provider_config` value guard rejects only selector-string-breaking chars (`"` `\` newlines) so real UI labels (parentheses, apostrophes) and legacy `UBAG_PROVIDER_CONFIG` env overrides still work. The registry-dispatched mock adapter (`adapters/mock/ubag_mock_adapter`) honors `options.provider_config`, echoes model settings, and emits a deterministic `thread_bound` with a **flat** top-level `thread_ref` matching the gateway consumer + live engine, so the browser-free bind→resume round trip is CI-testable.
- **SDK/CLI/dashboard**: TS SDK gained `UbagModelSettings` (flat map) + `conversation_missing` + `listConversations`; Go SDK gained `ListConversations` (its REST client uses an untyped `JSON` map for the create body, so `model_settings`/`conversation_missing` need no struct change — the plan's assumption of a typed Go request struct did not match the code). CLI `create-job` gained `--model`/`--thinking`/`--conversation`/`--conversation-missing` (absent when omitted). Read-only dashboard `/conversations` page consumes `GET /v1/conversations` with an honest "not enabled" state on 501; it is reachable by URL but **not yet in the sidebar nav** — promotion is deferred because the dashboard e2e enforces the documented §24.2 17-page inventory (a separate spec decision).

Validation (targeted, per repo rules; operator runs full `pnpm check` / `pnpm test:v0:local`):

```
pnpm lint:schemas ; pnpm lint:openapi ; node tools/check-contracts.mjs
node packages/conformance/scripts/validate-fixtures.mjs   # 44 scenarios
pnpm check:sdk-freshness ; pnpm test:sdk                    # 69 TS + Go
pnpm test:cli ; pnpm test:adapter-registry                 # 3 / 21
pnpm --filter @ubag/dashboard check ; pnpm --filter @ubag/dashboard test  # 17
cd apps/gateway && go build ./... && go vet ./... && go test ./...  # green except the pre-existing Windows python-alias env test
PYTHONPATH="apps/worker;adapters/mock" python -m pytest apps/worker/tests adapters/mock/tests  # 357 + 8
```

Known limitations / follow-ups: gRPC and the Go SDK cannot carry `model_settings` as typed fields (proto has no field; Go SDK is map-based) — HTTP is the typed surface. Live-provider verification (real Gemini/DeepSeek model pickers, chat resume) is ToS-bound and not CI-tested; it happens manually in production. Dashboard nav promotion + §24.2 update is a follow-up. Later roadmap slices: provider expansion (Kimi/Minimax/Claude activation), automatic provider fallback/routing, mobile push alerting.

## 2026-07-16 Fix: multi-turn conversation recall (resume hydration + turn-aware read)

Live testing of conversation affinity found multi-turn recall broken by **two independent bugs** in `apps/worker/ubag_worker/live/page_driver.py` (both live-only, `# pragma: no cover`; the mock driver is scripted and unaffected). Verified end-to-end against real ChatGPT after the fix: two jobs sharing a `conversation_id` — turn 1 "remember BANANA, reply OK" → `OK` (binds thread); turn 2 "what was the codeword?" → **`BANANA`** (resumes + recalls).

1. **Turn-aware read.** The response reader used `_first_visible` → `locator(sel).first`, but `response_container` matches **every** assistant turn (e.g. `div[data-message-author-role='assistant']`). On a resumed thread `.first` is the OLDEST turn's answer — already rendered and stable, so streaming settled instantly on it and the second turn returned the first turn's answer. Fix: `submit_prompt` snapshots per-candidate `response_container` counts **before** submitting (baseline of prior turns); `stream_response` waits (paced poll, no busy-spin) until a candidate's count exceeds its baseline — this turn's node has appeared — then binds `.last` (newest); `read_final_response` reads `.last` via a new `_newest_visible`. Fresh chat → baseline 0 → `.last == .first` → single-turn byte-identical. Times out into `DriftDetectedError` (fail loud) rather than returning a prior turn. DeepSeek's `final_answer_container` had the same `.first`-across-turns flaw, also fixed.

2. **Resume hydration wait.** `resume_thread` confirmed a bound thread loaded with the *short* warm-reuse emptiness probe (`0.8s` settle + `1.5s` one-shot). But providers hydrate a `/c/<id>` conversation's earlier messages via async JS **seconds** after `domcontentloaded` — measured **~6.6s** for ChatGPT — so on a cold thread the probe found nothing and wrongly broke the binding (`conversation.thread_broken` → `UBAG-TARGET-CONVERSATION-NOT-FOUND-001`); it only "worked" earlier when the thread happened to be warm/fast. Fix: new `_await_prior_turn` polls every `response_container` candidate against ONE shared deadline (`_resume_confirm_ms()`, default `20s`, env `UBAG_RESUME_CONFIRM_MS`), returning True as soon as a prior turn renders and False (bounded, not a hang) if none appears.

Both covered by `apps/worker/tests/test_conversation_turn_read.py` (fake Playwright pages: 5 read tests incl. deferred-reveal + single-turn regression; 4 resume tests incl. deferred-hydration + dead-thread + empty-ref). Full worker suite green (366). Note: `resume_thread`/`_await_prior_turn` and the CDP-attach in `open()` are not reachable in CI; environmental caveat — the bridge Chrome can wedge for CDP attach when parked in a Google sign-in intercept, unrelated to the worker (reproduced with a raw Playwright client), cleared by relaunching the bridge Chrome.

## 2026-07-16 Harden live-browser login persistence

Goal: the operator's ChatGPT/DeepSeek/Gemini logins should survive restarts/crashes and never force a re-login. Changes in `tools/live-browser/bridge.mjs`:

- **Clean-exit reset before every cold launch** (`hardenProfileForPersistentLogin`): patch the profile's `Default/Preferences` to `profile.exit_type=Normal` / `exited_cleanly=true` and `session.restore_on_startup=1`, so Chrome never opens in crash-recovery mode (which drops state) and never pops the "didn't shut down correctly" bubble over the login UI in the screencast. This makes a hard kill of Chrome (used to clear a wedged CDP attach) recover cleanly next launch. Note: Chrome rewrites `restore_on_startup` out of the file at runtime (it re-derives it), so session-cookie retention is best-effort; the durable logins ride on the persistent auth cookies below, not this pref.
- **Stale-lock cleanup**: remove `SingletonLock`/`SingletonCookie`/`SingletonSocket`/`lockfile` before a cold launch so a relaunch after a hard kill is never blocked by "profile in use".
- **`--hide-crash-restore-bubble`** flag as belt-and-suspenders for the bubble.
- **Chrome spawned detached + unref**: it now outlives the bridge, and the bridge's SIGINT/SIGTERM handlers leave it running, so restarting the bridge no longer signs the operator out (previously Chrome was a child of the bridge and died with it). The next bridge start re-attaches via `cdpAlive()`.

What actually keeps providers signed in is the persistent auth cookies in `Default/Network/Cookies`; the worker never launches a competing Chrome (it attaches over CDP and reuses the authenticated context), so jobs can't corrupt or lock the profile. **Verified**: after a full bridge+Chrome restart, all three providers (ChatGPT/DeepSeek/Gemini) probed as still logged in via their `authenticated_signal` selectors. Operator note: tick "stay signed in"/"keep me signed in" at login so the auth cookie is long-lived; server-side session expiry is provider policy and outside UBAG's control.

## Current Phase

v0/v2.1 platform baseline: contracts, gateway, gateway executor dispatch boundary with file-spool and NATS worker result ingestion, memory/Postgres/SQLite gateway stores, NATS JetStream executor, MinIO/localfs artifact storage with idempotent mutations, signed webhook outbox delivery, built-in template catalog/application/rendering, scoped cross-job events, paginated operator collections, hardened payload secret-key detection, edge queue/store contracts, worker/adapters, gateway-wired dashboard, CLI, TS/Go SDK wave, security/compliance contracts, observability contracts, and small-profile deployment scaffolding.

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

## 2026-06-18 Production Operator Activation Pass

Completed directly against production (`ubag.polytronx.com` / `/opt/ubag`) after the live-browser stack was already active:

- Inspected production containers, logs, DB topology rows, and gateway operator APIs. All core containers are up/healthy: gateway, nginx-dashboard, postgres, minio, dragonfly, browser-viewer, and the new browser-topology-sync service. Host `/opt` disk is high at roughly 85% used and should be cleaned before larger deploys.
- Fixed operator Jobs UX by adding a valid UBAG envelope submitter for ChatGPT, Gemini, and DeepSeek targets, with provider login-state badges from `/v1/browser/contexts`, template selection, prompt entry, and production job creation through `/v1/jobs`.
- Fixed existing dashboard action bugs: cancel and retry now call the gateway's real `/v1/jobs/{id}/cancel` and `/v1/jobs/{id}/retry` routes instead of the old colon-style paths.
- Wired Workflows UX so the operator can create a provider workflow and run an existing workflow through `/v1/workflows` and `/v1/workflows/{id}/runs`.
- Continued the Workflows UX with an ordered chain mode that creates steps in the requested provider order: ChatGPT, then Gemini, then DeepSeek. The form still supports single-provider workflows, and it shows live provider readiness so the operator can see that ChatGPT is currently manual-login pending while Gemini and DeepSeek are authenticated.
- Replaced one-shot/manual browser topology registration with `browser-topology-sync`, an idempotent recurring production service that reruns `register-browser-topology.sh` every `UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS` seconds. This keeps Browser Sessions repopulated after browser/gateway/database restarts without asking an agent to insert rows manually.
- Deployed the rebuilt dashboard bundle with `UBAG_BASE_PATH=/dashboard`, updated production Compose/scripts/docs/checker files, recreated nginx-dashboard, and started `browser-topology-sync`.
- Production DB verification after deploy: 1 browser instance (`br_prod_browser_viewer`), 3 provider contexts, 3 tabs; Gemini and DeepSeek authenticated, ChatGPT still unknown/warming pending the operator's manual ChatGPT login.
- Production API/UI smoke: `/v1/health`, browser topology, targets, adapters, jobs, templates, and workflows returned through nginx; `/v1/ready` remains intentionally blocked at the edge by nginx. Jobs, Workflows, and Browser Sessions rendered successfully in a headless browser against `https://ubag.polytronx.com/dashboard/...`.
- Follow-up production UI smoke verified the Workflows page renders `Ordered chain`, `Single provider`, `ChatGPT -> Gemini -> DeepSeek`, and provider readiness states.
- Safe write-path smoke used only the built-in `mock` target: job `job_000000000001` was accepted/queued, workflow `wfd_6d78879ffd80099234a51848` was created, and workflow run `wfr_e198f2fa93daa73b20f1a810` succeeded with job `job_000000000002`.
- Failed-job debug result: there were no failed jobs in `gateway_jobs` at inspection time.

Validation run locally before deploy:

```powershell
cmd /c pnpm --filter @ubag/dashboard check
cmd /c pnpm --filter @ubag/dashboard test
$env:UBAG_BASE_PATH='/dashboard'; cmd /c pnpm --filter @ubag/dashboard build
cmd /c pnpm test:deployment
git diff --check
```

Operational notes:

- Production nginx intentionally blocks `/v1/ready` and `/v1/metrics` at the public edge; use `/v1/health` externally and internal container healthchecks for readiness.
- noVNC/browser automation remains manual-login only. Do not automate provider login, CAPTCHA, 2FA, credential collection, cookie extraction, or storage-state extraction.
- Nginx access logs show noisy unauthenticated crawler hits to random `/shop/...` paths returning the ingress text response; not currently a gateway error, but it is a cleanup/hardening candidate if crawler noise matters.

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
| SDK wave 1 | Complete | `cmd /c pnpm test:sdk` validates generated operation-level contract manifest freshness plus TypeScript/JavaScript and Go SDKs against shared fixtures for system, job, event, artifact, operator collection, webhook replay, workflow/template, cache, apps/devices/audit, metrics, and stream entrypoint surfaces. |
| Sidecar | Complete | `cmd /c pnpm test:sidecar` validates the loopback `@ubag/sidecar` health/proxy runtime, mutating-route idempotency generation including artifact PUT/DELETE, public-binding guard, factory loopback enforcement, and absolute-form proxy target hardening. |
| CLI | Complete | `cmd /c pnpm test:cli` builds/typechecks and tests health/ready/version/create/get/list/cancel/retry/SSE plus list-events/list-targets/list-adapters/list-apps/list-devices/list-audit-events/list-webhooks/list-artifacts/get-artifact/put-artifact/delete-artifact/replay-webhook/cache-status/metrics, help, diagnostics surface, adapter-test command, and mock-worker smoke. |
| Operator dashboard | Complete | `cmd /c pnpm test:dashboard` checks and builds the gateway-wired NAJM/Hallmark dashboard with CSP, no third-party font calls, responsive gates, accessible state fixtures, gateway-native browser topology fields, runtime-provided loopback noVNC embedding only, real template render output, and workflow metadata without fake fixture DAGs. |
| Small deployment profile | Complete | `cmd /c pnpm test:deployment` validates the small-profile config/static checks, including Postgres migration runner, MinIO least-privilege bootstrap, nginx-dashboard ingress, and optional profiles. |
| Observability contracts | Complete | `cmd /c pnpm test:observability` validates metrics, events, logs, smoke checklist, and health probes. |
| v0 test chain | Complete | `cmd /c pnpm test:v0` passes end-to-end, including gateway Go tests. |
| Plugin & adapter-registry checks | Complete | Root `test:plugins` (20/20) and `test:adapter-registry` (16/16) pass and are wired into `test:v0:local`. |

## 2026-06-18 Dashboard Completion Pass

Dashboard-only scope requested and completed. The SvelteKit dashboard now consumes the gateway browser topology response shape directly (`instance_id`, `context_id`, `tab_id`, `state`) instead of legacy `id/status` assumptions; noVNC iframes are only mounted when the selected browser instance exposes a runtime-generated loopback `http://` URL, and the dashboard no longer manufactures noVNC URLs from its configured gateway URL. Template preview renders the gateway `/v1/templates/{id}/render` `rendered` field rather than dumping the response envelope. The workflows page now uses real gateway workflow data only and displays a DAG only when the API response actually contains step details; the current list endpoint is shown honestly as workflow metadata and step count.

Validated:

```powershell
cmd /c pnpm --filter @ubag/dashboard check
cmd /c pnpm --filter @ubag/dashboard test
cmd /c pnpm --filter @ubag/dashboard test:e2e
cmd /c pnpm test:dashboard
```

## 2026-06-18 Production Live-Browser Topology Activation

Production-only live-browser activation was applied on `ubag.polytronx.com`.
The noVNC websocket ingress now supports the stock `/websockify` path, the
production VNC password was set by operator request, the browser viewer has
public egress for provider sites plus private-network CDP reachability, and the
small-profile deployment now includes an automatic `browser-topology-register`
service under the `live-browser` profile. The registrar idempotently upserts the
single production Chromium instance plus ChatGPT, Gemini, and DeepSeek provider
contexts/tabs so the dashboard does not depend on one-off manual database rows
after restarts or redeploys.

Production verification:

```text
docker compose --env-file deploy/small/env.local -f docker-compose.small.yml --profile live-browser run --rm --no-deps browser-topology-register
INSERT 0 1
INSERT 0 3
INSERT 0 3
browser topology registered for tenant tenant_edge

gateway_browser_instances count for tenant_edge = 1
gateway_provider_contexts count for tenant_edge = 3
gateway_browser_tabs joined count for tenant_edge = 3
```

Operational note: Gemini and DeepSeek were visually/login-state checked by the
operator in the production browser. ChatGPT remains marked `unknown`/warming
until the operator completes that login manually. UBAG must not automate
provider login, CAPTCHA/2FA, consent, credential collection, cookie extraction,
or storage-state exfiltration.

## 2026-06-18 Production Browser Sessions Dashboard Fix

Production Browser Sessions initially remained stuck on `Loading...` even after
the topology rows existed. Browser-console verification showed the live static
dashboard bundle was stale and still rendered `instance.id.slice(...)`, while
the gateway correctly returns `instance_id`. The dashboard source was already
partly aligned, and this pass tightened the browser page further for production:
tabs now display `conversation_id` as the URL fallback, context tab counts are
derived from the loaded tab list when the API omits `tab_count`, the first
instance auto-selects after load, and the xterm welcome block no longer writes
twice.

Validated and deployed to production:

```powershell
cmd /c pnpm --filter @ubag/dashboard check
cmd /c pnpm --filter @ubag/dashboard test
$env:UBAG_BASE_PATH='/dashboard'; cmd /c pnpm --filter @ubag/dashboard build
```

Production verification with Chrome against
`https://ubag.polytronx.com/dashboard/browser` returned HTTP 200 for all four
browser API calls and rendered: 1 instance, 3 contexts, 3 tabs; context rows show
1 tab each; ChatGPT is warming, DeepSeek and Gemini are ready. Cloudflare's
injected beacon is blocked by the dashboard CSP, but that is unrelated to UBAG
runtime and does not block the page.

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

Superseded limitations from this checkpoint: later v2.1 slices added gateway
SSO sessions, Exclusive XML-C14N SAML verification, native Postgres stores for
response cache/workflow/SSO/SCIM/SIEM/webhook-secret/session/audit/alert/topology
state, and real Merkle-chained audit export. SDK support is intentionally
limited to TypeScript/JavaScript (`@ubag/sdk`) and Go
(`github.com/ubag/ubag-go`). Prior non-TS/Go SDK package trees were removed
from the active workspace; Git history is the archive if those ecosystems are
revisited later. Live provider adapters still require real accounts/sessions
and remain externally blocked.

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
- Small-profile edge ingress blocks unauthenticated public `/v1/metrics*` and `/v1/ready*` while keeping private-network Prometheus/gateway probes available.
- Small-profile deployment hardening now includes an explicit rerunnable Postgres `migrate` action, a `minio-init` least-privilege artifact user/policy bootstrap, separate MinIO root and gateway credentials, and nginx-dashboard ingress for dashboard/API/noVNC routes.
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
- TypeScript/JavaScript and Go SDKs for jobs, system endpoints, workflow/template list endpoints, cache status, apps/devices/audit, metrics, artifact get/put/delete, and SSE helpers with generated contract-manifest freshness checks.
- CLI, loopback sidecar with idempotency auto-generation for mutating proxy routes, gateway-wired dashboard, observability package, and small-profile deployment scaffolding.
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
- `test:sdk` validates generated contract freshness plus TypeScript/JavaScript and Go SDKs.
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
- SDK freshness: `cmd /c pnpm check:sdk-freshness` passed for TypeScript/JavaScript and Go generated contract manifests.
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

### 2026-05-31 — v2.1 follow-ups committed

- Wired worker `concurrency.cap_changed` telemetry to the gateway `ConcurrencyRegistry` (intercepted in the `WorkerConsumer` loop, recorded via `topology.ConcurrencyRegistry.Report`). Gateway + worker unit tests added and green.
- Added `tools/run-postgres-roundtrip-tests.mjs` (`pnpm test:gateway:postgres`) with a false-green guard and `docs/postgres-roundtrip-tests.md`.
- Added the ToS-safe live-provider template `live_web_template(...)` + registered `generic_live_web`, with `apps/worker/ubag_worker/live/ONBOARDING.md`.
- Validation (all exit 0): `node tools/run-go-tests.mjs apps/gateway`, `node tools/run-python-worker-tests.mjs` (122 tests + smoke), `pnpm run check`, `pnpm test:v0:local`.
- Git: baseline `0364595` (v0) + new delta commit `85d6eb0` (v2.1). Worktree clean; not pushed.

Before any future implementation slice, run:

```powershell
git status --short --branch
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

Next coding queue is documented in `AGENT_HANDOFF.md`. Update this ledger and the handoff file whenever implementation scope, validation evidence, runtime status, or remaining work changes.

### 2026-06-17 - TS+Go SDK-only completion

Implemented the TS+Go-only SDK policy for the active repository. The supported
SDK set is now TypeScript/JavaScript (`@ubag/sdk`) and Go
(`github.com/ubag/ubag-go`) only. Prior Python, Rust, Java, Kotlin, Ruby, PHP,
C#, Swift, and Elixir SDK package trees were removed from active source,
scripts, CI, docs, packaging, and release claims; Git history remains the
archive. `packages/sidecar-rust` remains active because it is the loopback
sidecar, not an SDK.

Completed changes:

- Root SDK scripts now run generated-manifest freshness plus TypeScript and Go tests only.
- `tools/make-sdks/generate-manifest.mjs` is the canonical TS+Go manifest generator and supports non-mutating `--check` mode.
- `tools/check-contracts.mjs` uses the generator check mode instead of mutating generated files during contract validation.
- Stale nested npm lockfiles were removed and `pnpm-lock.yaml` was refreshed for the current pnpm workspace.
- Active docs, rendered docs, Superpowers SDK plan/spec notes, CI, Makefile, SDK/conformance docs, and licensing now describe TS+Go-only SDK support.
- Dashboard, worker, and small-deployment validation blockers found during the pass were fixed without changing the product scope: dashboard SvelteKit sync/types and static-adapter nav assertions, worker test `PYTHONPATH`, and small-profile nginx-dashboard deployment checks/docs.

Validation passed:

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

Runtime note: Docker Compose is not installed on this host, so
`cmd /c pnpm test:deployment` explicitly skipped compose rendering and passed
the static small-deployment checks.

### 2026-06-02 — Live-browser viewer (noVNC) admin login stack

Added an opt-in `live-browser` Compose profile so a super-admin can complete the **manual, human** login/CAPTCHA/2FA flow for AI providers inside a real, persistent Chromium streamed to the dashboard over noVNC. This is the ToS-safe substrate for activating live web adapters: UBAG never captures credentials, cookies, or storage state, and never solves challenges — the operator logs in by hand and the worker attaches to the already-authenticated profile over CDP.

Completed and locally validated (additive, opt-in, default path unchanged):

- **`deploy/small/browser-viewer/Dockerfile` + `entrypoint.sh` (new).** `debian:bookworm-slim` running `Xvfb` + `fluxbox` + `chromium` (`--remote-debugging-port=9222`, `--user-data-dir=/profiles/default`) + `x11vnc` (`-localhost -rfbauth`) + `websockify`/`noVNC` on `6080`. Entrypoint requires `UBAG_BROWSER_VNC_PASSWORD` (fails closed), uses LF line endings, and runs under `tini`.
- **`docker-compose.small.yml`.** New `browser-viewer` service under `profiles: ["live-browser"]` on the internal `ubag-private` network; only loopback noVNC (`${UBAG_NOVNC_PORT:-7900}:6080`) is published — CDP `9222` stays internal. Persistent `browser_profiles` volume. Gateway gains `UBAG_REMOTE_BROWSER_ENDPOINT`, `UBAG_BROWSER_HEADED`, `UBAG_BROWSER_ENGINE`, `UBAG_BROWSER_PROTOCOL`, `UBAG_NOVNC_BASE_URL` passthrough.
- **Small-profile edge ingress.** New `/novnc/*` reverse proxy to `browser-viewer:6080` with `X-Frame-Options: SAMEORIGIN` override so the dashboard can embed the viewer iframe (global header default is `DENY`).
- **Worker noVNC URL is now operator-configurable (`apps/worker/ubag_worker/live/engine.py`).** `_novnc_url` reads `UBAG_NOVNC_BASE_URL` and only honors **loopback** `http://host:port` bases via the new `_is_loopback_novnc_base` guard; any non-loopback/scheme/path value falls back to the default `http://127.0.0.1:7900`, so the gateway's loopback-only forwarding contract holds and existing tests keep their exact URL.
- **Dashboard Take-control viewer (`apps/dashboard/src`).** Browser panel gains a `.live-viewer` region with **Take control** / **Open in new tab** / **Release** controls; the noVNC iframe is lazily mounted (sandboxed `allow-scripts allow-same-origin allow-forms`) only on demand. CSP gains `frame-src 'self'`. No credential/cookie/storage-state surface is added — storage stays a boolean indicator.
- **Config + docs.** `deploy/small/env.example` documents the new vars; `deploy/small/README.md` adds a "Live-browser viewer (noVNC)" section covering the loopback/password posture; `tools/check-small-deployment.mjs` asserts the compose service, Dockerfile, entrypoint, Caddy route, and env keys.

Tests added (all green):

- Worker `apps/worker/tests/test_novnc_base_url.py` — 7 tests: default unchanged, loopback/localhost overrides honored, non-loopback/https/with-path fall back to default, and the `_is_loopback_novnc_base` predicate (accepts `127.x`/`localhost`, rejects routable hosts, bad schemes, missing port, out-of-range port).

Validation (all true exit 0):

- `node tools/run-go-tests.mjs apps/gateway` — all packages green.
- `node tools/run-python-worker-tests.mjs` — 150 worker tests (143 prior + 7 new) + 5 + smoke (16 JSONL events).
- `cmd /c pnpm check` — green (dashboard redaction guards + docs responsive).
- `cmd /c pnpm test:deployment` — green (`docker compose config` validated the new service + all static term checks).
- `cmd /c pnpm test:v0` — green (includes the gateway Go suite).

Honest limitations (ToS-bound): the **live real-browser provider path cannot be CI-validated** — manual human login is required and automated real-provider runs are forbidden. The `browser-viewer` Docker image was **not** built/run here (no guaranteed Linux Docker engine on this host); only the static Compose/Caddy/Dockerfile config and the worker/dashboard wiring were validated via offline/mock drivers, unit tests, and `docker compose config`. noVNC URLs remain runtime-generated, loopback-scoped, and VNC-password-gated; client-supplied noVNC URLs are rejected/redacted by the gateway.

### 2026-06-01 — Worker runtime orchestration integration (Option A, full)

The v2.1 multi-tab/concurrency/cross-engine orchestration algorithms (Fleet, ChannelPool, AIMD, WeightedScheduler, topology) were previously a unit-tested library not wired into the live runtime. This pass performs the full, backward-compatible integration so the live worker path can emit adaptive-concurrency and browser-topology telemetry, and the gateway projects topology snapshots into its in-memory topology store.

Completed and locally validated (additive, opt-in, default path byte-identical):

- **Engine selection wired into the driver (`apps/worker/ubag_worker/live/page_driver.py`).** `PlaywrightPageDriver(engine_spec=None)` plus a pure, unit-testable `_resolve_launch_plan(engine_spec, headless) -> _LaunchPlan(browser_type_name, remote_endpoint, headless)` helper. `create_default_driver` now resolves `engine_spec_from_env()`; default env (`chromium`/local/headless) yields unchanged behavior. Firefox/WebKit/BiDi/remote-endpoint/headed variants are honored. Playwright calls remain `pragma: no cover`.
- **New `apps/worker/ubag_worker/live/orchestrator.py`.** `LiveOrchestrator` composes a `Fleet` and per-`(tenant, provider, identity)` `ChannelPool` with a **persistent** AIMD controller (survives leases within a process), thread-safe with an injectable clock. `lease(...) -> LiveLease(pool, tab, context, result)`; `record_outcome(lease, success, signal) -> Optional[CapChange]`; `concurrency_state(lease)`; `topology_snapshot(tenant_id=None) -> {"instances", "contexts", "tabs"}`. Snapshots never include a storage-state URI (boolean only).
- **`LiveSessionEngine` routing (`apps/worker/ubag_worker/live/engine.py`).** `__init__` gains optional `orchestrator=None`. When set, a job leases a tab from the orchestrator and the engine emits `browser.topology_reported` (canonical position after `running`/before token events) and, on an AIMD `CapChange`, a `concurrency.cap_changed` trailer. The manual-login-blocked path returns before leasing (no topology/concurrency events). With `orchestrator=None`, output is byte-identical to the legacy path, so all pre-existing worker tests stay green.
- **Gateway topology ingestion (`apps/gateway/internal/executor/workerconsumer.go`).** New const `browser.topology_reported`; optional nil-safe `Topology topology.TopologyIngestor` field (interface `AddInstance`/`AddContext`/`AddTab`, satisfied by `*topology.MemoryStore`). `RunOnce` intercepts the event and `continue`s before `ApplyWorkerEvent` (poison-safe). The consumer **forces** `TenantID = job.TenantID` on instances/contexts (tenant isolation) and `HasStorageState = false` on contexts, ignoring any worker-supplied values. Wired in `main.go` only when the default topology store is `*MemoryStore`; SQLite/Postgres topology stores yield a `nil` ingestor and are untouched (matches the "worker writes tables, gateway reads" doc contract — event ingestion is an in-memory-only convenience).

Tests added (all green):

- Worker `apps/worker/tests/test_live_orchestration.py` — 21 tests: launch-plan resolution (7), `LiveOrchestrator` lease/outcome/AIMD-persistence/tenant-isolation/storage-state-redaction/concurrency-state/injected-Fleet (8), and `LiveSessionEngine`-with-orchestrator event emission incl. drift cap-change trailer and manual-login no-lease (6).
- Gateway `apps/gateway/internal/executor/workerconsumer_test.go` — `TestWorkerConsumerProjectsTopologyReport` (projects instance/context/tab, overrides spoofed tenant + `has_storage_state`, job still completes) and `TestWorkerConsumerTopologyRecordingIsNilSafe` (no ingestor configured → event dropped, job processes).

Validation (all exit 0):

- `node tools/run-go-tests.mjs apps/gateway` — all packages green (executor re-ran with new tests).
- `node tools/run-python-worker-tests.mjs` — 143 tests (122 legacy + 21 new) + 5 + smoke, true `EXIT=0`.
- `cmd /c pnpm check` — green.
- `cmd /c pnpm test:v0` — green (includes the gateway Go suite).

Honest limitations (unchanged, ToS-bound): the **live real-browser provider path cannot be CI-validated** — ToS forbids automated real-provider runs and the live path requires a real browser with manual human login. All new wiring is validated exclusively via offline/mock drivers, fakes, and unit/structure tests, **not** live provider runs. The gateway topology-event ingestion is in-memory-only by design; durable topology persistence remains the documented worker-writes-tables path.
