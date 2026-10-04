# UBAG A-Z Implementation Coverage

Last updated: 2026-07-24

The canonical rendered version is in the docs site at `apps/docs/src/content/docs/implementation-coverage.md`.

Current status: the repository contains implemented, runnable, or validateable artifacts for the current TS+Go product direction. The gateway includes gateway-side executable payload safety checks with secret-like key detection and secret-reference exceptions, an internal executor dispatch boundary with default `noop` mode, local file-spool dispatch, NATS JetStream dispatch/worker consumption, embedded worker consumer/result-ingestion, memory/Postgres/SQLite gateway stores, tenant/app-scoped cross-job event listing at `/v1/events`, paginated and authorized operator collections, MinIO/localfs artifact storage, signed webhook outbox delivery, a built-in template catalog with Pongo2-compatible render dry-runs, workflow definitions/runs, response cache, SSO/SCIM/SIEM/audit/session/topology stores, TypeScript/JavaScript and Go SDK freshness checks, CLI, sidecar, and a gateway-wired NAJM/Hallmark dashboard. Live-provider execution, production deployment, release publishing, and formal compliance certification require external activation inputs: user-owned provider accounts/manual browser sessions, Docker Desktop Linux engine or equivalent runtime, deployment host/DNS/TLS/secrets, release credentials, and legal/compliance review.

Continuation status: future agents should start with `AGENT_HANDOFF.md`, then `PROGRESS.md`, then this coverage ledger. The handoff captures the current worktree state, latest green validation, fixed audit findings, runtime probe evidence, critical invariants, and next coding queue.

2026-07-24 jobs-list response compatibility. Dashboard job collections now
normalize the gateway's canonical `job_id` plus nested metadata shape before
rendering, while retaining legacy fixture compatibility. Malformed rows are
dropped instead of crashing the Svelte update and leaving `Loading...` visible.
The Jobs, Overview, and Failed/DLQ pages share the corrected boundary.

2026-07-24 dashboard overview resilience. The metric cards and Recent Activity
now share one jobs-list response, removing the duplicate-request split state
that could leave activity loading after the cards rendered. Dashboard requests
have a 15-second abort guard and render a timeout error instead of spinning
indefinitely. Focused dashboard tests, Svelte diagnostics, and one targeted
dashboard build passed; no broad suite or CI ran.

2026-07-24 attachment production release. The full attachment surface is merged,
pushed, and deployed with the warm daemon enabled. Focused unit/type checks,
one targeted dashboard build, and the Chromium jobs-page check at
320/375/414/768 passed. Production acceptance completed for text compatibility,
the legacy audio alias, ChatGPT multipart and warm reuse, DeepSeek document
key-reference, Gemini multipart document+WAV, adapter catalog policies, and the
deployed dashboard bundle. DeepSeek's current live composer supports
documents/images only, so audio/voice/video now fail closed at create time rather
than hanging a worker.

Primary validation command:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
```

Latest 2026-06-17 continuation validation includes `cmd /c pnpm install --frozen-lockfile`, `cmd /c pnpm test:v0`, `cmd /c pnpm check`, `git diff --check`, plus focused checks for gateway, SDK, conformance, schemas, deployment, observability, CLI, sidecar, dashboard, contracts, and SDK freshness.

Conformance coverage is currently 45 executable REST scenarios plus 286 named non-executable coverage scenarios, including executor dispatch, file-spool/NATS worker ingestion, webhook outbox retry, and the v2.1 observability surfaces (browser topology, adaptive concurrency, manual-action alerts, audit-export Merkle chain, SSO logout, cross-engine grids, and Postgres persistence). Counts derived by enumerating `packages/conformance/fixtures/v0/scenarios.json`: 45 entries in `scenarios` (each run by `pnpm test:conformance` via `@ubag/conformance validate`) and 286 entries in `coverage_scenarios` (the declared, named non-executable coverage list); `tools/check-contracts.mjs` asserts both arrays exist and pins the executable floor at 8.

2026-07-23 Gemini model-policy hardening. The live Gemini adapter now reasserts `3.6 Flash` before every prompt and models Standard thinking as the independently persisted `Extended thinking` toggle being off. Production DOM inspection confirmed both selections can otherwise remain active at once. The existing required-setting drift path fails closed rather than submitting when the model or thinking state cannot be verified. Production image deployment is healthy, and live job `job_000000000028` completed with selector version `2026-07-23-gemini-3.6-standard` and exact output `UBAG_GEMINI_36_STANDARD_OK`.

2026-06-19 RadioPad UBAG production live-provider hardening. Production `https://ubag.polytronx.com` now forwards the required non-secret live-browser runtime env into the worker subprocess, uses a 120000 ms live-worker runtime budget, flushes live-worker JSONL per event, avoids closing the shared CDP browser context, accepts `session.opening`/`session.authenticated` worker events, and includes current DeepSeek DOM fallbacks for authenticated composer detection and send-button selection. Production evidence: RadioPad backend `radiopad-api` reached UBAG `/v1/browser/summary` with HTTP 200; DeepSeek production API smoke `job_000000000017` completed with `deepseek_web OK`; Gemini production API smoke `job_000000000018` authenticated and safely blocked with `manual_consent_or_overlay_required`, requiring manual operator action in noVNC. Validation passed: focused worker live-adapter/navigation tests, focused gateway env/event-ingestion tests, and small-deployment checks. UBAG still does not automate provider login, consent, CAPTCHA, 2FA, credential collection, cookie extraction, or PHI handling.

2026-06-02 live-browser viewer (noVNC) admin login stack (additive, opt-in `live-browser` Compose profile). A new `browser-viewer` service (`deploy/small/browser-viewer/Dockerfile` + `entrypoint.sh`: Xvfb + fluxbox + Chromium with CDP on `9222` + x11vnc + websockify/noVNC on `6080`) lets a super-admin complete the **manual, human** provider login/CAPTCHA/2FA inside a real persistent Chromium streamed over noVNC. noVNC is published loopback-only (`UBAG_NOVNC_PORT`, default `7900`) and VNC-password-gated (`UBAG_BROWSER_VNC_PASSWORD`, fails closed); CDP stays on the internal network; the profile persists on a `browser_profiles` volume. Caddy exposes `/novnc/*` (SAMEORIGIN) and the dashboard adds a lazy, sandboxed **Take control** iframe (`frame-src 'self'`). The worker `_novnc_url` is now `UBAG_NOVNC_BASE_URL`-configurable but loopback-gated (`_is_loopback_novnc_base`), falling back to `http://127.0.0.1:7900` so the gateway's loopback-only forwarding contract holds. UBAG never captures credentials/cookies/storage-state and never solves challenges. Validation (all true exit 0): `node tools/run-go-tests.mjs apps/gateway`, `node tools/run-python-worker-tests.mjs` (150 tests + smoke), `cmd /c pnpm check`, `cmd /c pnpm test:deployment` (`docker compose config` validated the new service), `cmd /c pnpm test:v0`. New tests: `apps/worker/tests/test_novnc_base_url.py` (7). Honest limitation (ToS-bound): the live real-browser provider path cannot be CI-validated and the `browser-viewer` image was not built/run here (no guaranteed Linux Docker engine); only static Compose/Caddy/Dockerfile config and worker/dashboard wiring were validated.

2026-06-01 worker runtime orchestration integration (Option A, full; additive and opt-in). The v2.1 orchestration algorithms (Fleet, ChannelPool, persistent AIMD, WeightedScheduler, topology) are now wired into the live worker path: `LiveSessionEngine` accepts an optional `LiveOrchestrator` (`apps/worker/ubag_worker/live/orchestrator.py`) that leases tabs and emits `browser.topology_reported` + `concurrency.cap_changed`, and `create_default_driver` honors `engine_spec_from_env()` via a pure `_resolve_launch_plan` helper. The gateway `WorkerConsumer` intercepts `browser.topology_reported` and projects instances/contexts/tabs into its in-memory `topology.MemoryStore`, forcing the job's tenant and redacting storage-state to a boolean (poison-safe, nil-safe; SQLite/Postgres topology stores are untouched). With `orchestrator=None` and no `Topology` ingestor the legacy path is byte-identical. Validation (all exit 0): `node tools/run-go-tests.mjs apps/gateway`, `node tools/run-python-worker-tests.mjs` (143 tests + smoke), `cmd /c pnpm check`, `cmd /c pnpm test:v0`. New tests: `apps/worker/tests/test_live_orchestration.py` (21) and gateway `internal/executor/workerconsumer_test.go` topology tests. Honest limitation (ToS-bound): the live real-browser provider path cannot be CI-validated; all new wiring is validated via offline/mock drivers, fakes, and unit/structure tests only.

2026-05-29 gateway runtime + enterprise surface update (code-complete & locally validated; all `apps/gateway` `go build`/`vet`/`test ./...` green on Go 1.26). The gateway now wires runtime SQLite stores (`UBAG_GATEWAY_STORE=sqlite`, WAL/`busy_timeout`/`foreign_keys`/single-writer), a localfs artifact store (`UBAG_ARTIFACT_STORE=localfs`, `UBAG_ARTIFACT_DIR`), and a SQLite webhook outbox mode, plus six enterprise leaf packages each with passing Go tests: `internal/ratelimit` (memory/SQLite/Postgres), `internal/responsecache` (memory/SQLite, never exposes cached payload values), `internal/workflow` (memory/SQLite multi-step runs with payload policy per step), `internal/sso` (stdlib OIDC RS256 + SAML verification, memory/SQLite), `internal/scim` (SCIM v2 Users/Groups, memory/SQLite, passwords never stored), and `internal/siem` (redacted File/HTTP/Syslog export). HTTP wiring is nil-safe/optional and adds `/v1/cache`, `/v1/rate-limits`, `/v1/workflows[/runs]`, `/v1/sso/config` + OIDC/SAML callbacks, `/v1/scim/v2/{Users,Groups}`, `/v1/siem/config` + `/v1/audit/export`, and `/v1/webhooks/secret:rotate`, gated by the corresponding RBAC actions, with new env vars `UBAG_RATE_LIMIT_ENABLED` (default false), `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`, and `UBAG_SIEM_FILE_PATH`. Independent review PASSED with no Critical/High issues; cache purge returns `501` when disabled and SSO config `PUT` rejects OIDC without an Issuer and SAML without an IdP cert. The gRPC + grpc-web layer was completed in a previous slice.

Honest limitations / externally-blocked follow-ups: SSO OIDC/SAML callbacks now mint a revocable, server-side gateway session (memory/SQLite/Postgres `gateway_sessions`), validated per request and revoked on `POST /v1/sso/logout`; SAML signature verification uses Exclusive XML Canonicalization (`http://www.w3.org/2001/10/xml-exc-c14n#`, `internal/sso/canonicalize.go`) and fails closed; native Postgres stores now exist for rate-limiter, response cache, workflow, SSO config, SCIM, sessions, audit, alerts, and topology (in-memory remains an opt-in fallback); `POST /v1/audit/export` streams the persisted Merkle-chained audit records (`records[]`, `head_hash`, `count`) with a `chain_valid` integrity proof; TypeScript/JavaScript and Go are the only active first-class SDKs; and live provider adapters remain externally-blocked until user-owned account sessions are available.

## 2026-10-05 production gap repair checkpoint

Perplexity removed from active adapters/catalogs/selectors/tooling. Matching
browser/gateway images at 1de7727 deployed successfully (Gateway Image run
37232300132). Conversations now enabled; noVNC connects through authenticated
websockify. Browser PID cap raised from inherited 256 to explicit 1024 after
confirmed pthread_create EAGAIN; memory caps now match source configuration.
Fresh DuckAI (1032), DeepSeek (1034), Gemini (1035) audit jobs completed.
18 authenticated dashboard routes rendered without JavaScript errors; 320/375/
414/768 widths passed overflow checks. ChatGPT/Mistral live acceptance pending.
Signed webhook canary 1036 exposed PostgreSQL LeaseDue RETURNING ambiguity
(SQLSTATE 42702). Fixed the CTE update to avoid a joined id ambiguity; added an
isolated temporary-table PostgreSQL regression proving leasing and exclusion of
active leases. Targeted webhook Go tests pass locally (real-Postgres tests are
DSN-gated); CI must execute the new PostgreSQL test before acceptance.
Quota page called nonexistent /v1/quotas and /v1/billing. It now displays the
real /v1/concurrency current_cap/in_flight data with existing NAJM states/layout.
Svelte check: zero errors, two existing LiveBrowser state-capture warnings.
This follow-up is not deployed yet; webhook/live-provider final checks pending.


ChatGPT readiness follow-up: the VPS composer matches
`div[contenteditable=true][data-virtualkeyboard=true]` but all three old auth
markers match zero nodes and no login signal exists. The worker therefore
waited for human login instead of sending a prompt. Added the observed current
composer to readiness signals and versioned the baseline; 29 adapter tests and
provider consistency check pass. Mistral has a visible Sign in wall and no auth
markers; manual human login requested via Browser Sessions. Do not bypass it.


Final acceptance checkpoint before menu repair deployment:
- Revision fef1570 deployed; Gateway Image 37233948951 and CI 37233948858
  succeeded. CI ran TestPostgresStoreLeaseDueDoesNotLeaseTwice against Postgres.
- Signed webhook job 1041 delivered once with HTTP 204; isolated receiver
  confirmed a fresh valid HMAC signature. Old callback job 1036 received a
  generic ingress 200 while its temporary route was absent during replacement;
  that response alone is NOT signature-verification evidence.
- Quotas & Limits deployed, no failed API calls/JS errors; no overflow at
  320/375/414/768; authenticated noVNC RFB session connected.
- ChatGPT job 1040 reached authenticated/running and then reported
  setting:model drift. Live menu options/selection were correct; native click
  failed because the visible radio menu row was covered by the horizontal
  ViewTrack/Track overlay. Added direct dispatch restricted to visible radio
  menu items; _ensure_setting still verifies selection and fails closed.
  Ordinary controls/login/prompt actions keep normal pointer checks. Two tests
  cover that boundary; provider-config tests 21 passed.
- A subsequent push interrupted the previous CI deploy mid-replacement (the
  workflow had cancel-in-progress=true). Restored service through Compose and
  the final deploy completed. Changed the production workflow to queue pushes,
  so subsequent pushes cannot cancel a container replacement in progress.
- Mistral needs human Sign in; no credentials or CAPTCHA automation performed.


Revision 064f8fd deployed (Gateway Image 37234939541 success). Authenticated
Jobs form submitted DuckAI job 1042 (202); job details dialog opened and result
completed with the exact UI audit marker. ChatGPT 1043 completed and created an
active Postgres conversation binding. Its result included the outer UI label
"ChatGPT said", so the observed scoped MarkdownRoot child is now the primary
response selector (verified DOM contains the exact answer, no label).
CI 37234939579 failed solely on Ruff I001 in the new test imports; corrected
stdlib import ordering. Latest selector/import fix must pass CI and deploy;
then verify exact ChatGPT output and same-thread second turn. Mistral login
remains pending. Temporary webhook receiver and nginx audit route removed.


Final CI 37235460962 succeeded at 697a244; deployment 37235460961 succeeded
on retry after restoring the CI deploy script executable permission (source
archive synchronization had reset its mode; later sync excludes that script).
ChatGPT 1045 exact final answer AND exact token deltas passed; 1047 recalled the
same token from the same provider thread after conversation resume. Public
curl probes: dashboard/API/websockify 401, ready/metrics 404, healthz 200 and
origin healthz 200. A server-side Python urllib probe was Cloudflare-blocked
403 across all paths; actual Chrome UI and local curl remain functional.
Browser pids.events max=0 under explicit 1024 cap, all seven ready checks true.
Dashboard retry of our failed PID-limit canary created 1044, completed; separate
UI canary 1046 cancelled via dashboard (202/cancelled). No user jobs replayed.
The successful resumed turn exposed stale last_job_id/last_used_at: Touch()
existed but no worker completion called it. Extended the existing dispatch test
with activity assertions; it failed before the fix. Successful completion now
refreshes only the trusted tenant/app/target/conversation key; thread URL stays
unchanged. Four targeted conversation/consumer Go tests pass. Rollout/live
metadata verification pending; Mistral human login remains the only provider gate.


## 2026-10-05 production gap acceptance — final checkpoint

Deployed gateway/browser/reaper revision:
`a6bed82b00f22df1765e1e58acee211a812c0842`.
Gateway Image/deploy run 37236242548 SUCCESS; CI 37236242543 SUCCESS
(worker, gateway with real Postgres regression, SDK/dashboard/docs,
integration, contracts and supply-chain checks). Optional SSO and PR-only
Dependency Review jobs skipped; those are not accepted by this audit.

Accepted on production:
- Perplexity adapter absent in image and live VPS source; all active registry,
  selectors/catalogs/routes/tooling removed. No retired-provider env entries.
- Conversations API 200; real ChatGPT first/second/third turns 1045/1047/1048
  returned the exact nonce. Third turn resumed the SAME durable provider thread
  after browser/gateway replacement; binding last_job_id updated to 1048.
- Postgres webhook worker enabled; job 1041 delivery has one attempt, HTTP 204;
  disposable receiver verified fresh HMAC signature. Test route/receiver removed.
- Authenticated websockify/noVNC connects actual RFB, including final revision.
  Public websockify/dashboard/API challenge 401; ready/metrics remain 404.
- Four live providers returned fresh responses: ChatGPT, DuckAI, DeepSeek, Gemini.
  Retry of our PID-limit failure created 1044 and completed; original failures
  remain truthful historical records. No customer prompts were bulk-replayed.
- 18 authenticated dashboard routes rendered; actual UI submit/details/retry/
  cancel exercised. Conversation filter displayed our binding; limits page uses
  real concurrency API without 404/error panels. Widths 320/375/414/768 passed.
- Browser cap 1024, pids.events max=0; gateway/browser/nginx healthy, reaper up;
  readiness all seven true. VPS root 36% used, ample available memory.

Remaining acceptance gate: Mistral Sign in is visible. Manual operator login
requested and its page left open in the persistent browser, available through
Browser Sessions / Take control. No login/CAPTCHA/2FA automation or credentials
captured. Cannot claim every feature is 100% verified while this gate remains;
optional integrations/SSO/attachments/disaster recovery are outside these
specific repaired gaps and were not given blanket acceptance.

Cleanup complete: temporary Basic Auth audit account removed IN PLACE while
preserving existing operator accounts; removed audit credentials rejected 401;
local credential JSON deleted. Temporary webhook route/receiver, source-sync
archives and cancelled deployment config carrier removed. User sessions/data,
production environment secrets, backups and historical ledgers preserved.
Active VPS source synchronized; CI deploy script retains executable permission.

## 2026-10-05 latest-main redeployment and fresh E2E acceptance

User requested commit, push, deploy and production E2E verification.
Main application changes are pushed. Gateway Image run 37237136071 SUCCESS
deployed gateway/browser/reaper image revision
1972d01602fea59edbabac0dbdaf5a6ccf5931ae to 185.252.233.186.
Previous full CI run 37236242543 remains green; this latest revision only adds
the earlier acceptance report. This new entry is a documentation-only follow-up.

Fresh verification against that deployment:
- ChatGPT 1049 completed with the exact fresh nonce; resumed turn 1050 recalled
  it exactly in the SAME provider thread and updated conversation activity.
- DuckAI 1051, DeepSeek 1052, Gemini 1053 completed with exact fresh nonces.
- Signed webhook job 1054: delivered, attempt_count=1, HTTP 204. Disposable
  receiver verified current timestamp, nonce and HMAC over the actual payload
  for this job. Temporary nginx route restored and receiver stopped.
- Actual dashboard UI submitted DeepSeek 1055 with a small text attachment.
  The token was provided only in the file; returned text matched exactly.
  Job submission HTTP 202, completion and job-details dialog all passed.
- 18 authenticated dashboard pages HTTP 200. Mobile widths 320/375/414/768
  fit without horizontal overflow. Follow-up browser checks found no JS errors
  or failed /v1/ responses; actual noVNC RFB connection passed.
- /v1/ready 200, seven checks true; conversations/concurrency/adapters 200.
  Public healthz 200, dashboard/API/websockify 401, ready/metrics 404.
  Gateway/browser/nginx healthy; browser pids.events max=0.
- Perplexity image/source absent, retired-provider env keys absent.
  Deployment script mode remains 755.
- Temporary audit Basic Auth account removed in place, operator entries kept.
  Removed test credentials rejected 401; local credential JSON deleted.

Mistral still presents Sign in; its page is left open in the persistent browser
for manual operator login via Browser Sessions / Take control. Human-only login
rule prevents this provider from receiving blanket E2E acceptance. No credentials,
cookies or CAPTCHA handling automated. Optional SSO/integrations/disaster restore
remain outside these checks. Text attachment acceptance above is DeepSeek only;
it does not establish every file type on every provider. Historical jobs preserved.
