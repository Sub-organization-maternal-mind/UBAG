# UBAG Agent Handoff

Last updated: 2026-09-28

## REMAINING WORK — architecture-audit closeout (read this first, 2026-09-28)

The backend/API audit (34 findings, WS-1…WS-8) is implemented and pushed
through commit `79c6dee`. The dashboard ultra-fluid polish pass is fully
landed (3963e75). What is LEFT, in priority order:

1. **Verify CI is green on `79c6dee`.** The last three red gates were fixed
   in 79c6dee (pgvector/pgvector:pg16 service image for migration 0008's
   `vector` extension; CI apply-loop skips 0008 exactly like the production
   entrypoint's OPTIONAL_MIGRATIONS; KSV-041 annotation moved directly above
   `- secrets` in deploy/operator/config/rbac/clusterrole.yaml) but the run
   had not finished when the session closed. If red: check Gateway (Go)
   "Apply Postgres migrations" and Supply-chain "IaC scan" first — those
   were the two jobs still in play. The IaC annotations format is
   `# trivy:ignore:DS-0002 — <reason>` placed directly ABOVE the offending
   line (for KSV-0041: above `- secrets`); trivy-action@v0.36.0 honors them.
2. **Production deploy verification.** Gateway Image has been deploying
   every main push (GHCR sha-<commit> tags + deploy-gateway + deploy-dashboard
   via deploy/small/ci-deploy.sh). Verify prod: `grep UBAG_GATEWAY_IMAGE
   /opt/docker/ubag/deploy/vps/env.local` on 185.252.233.186 pins
   sha-<HEAD>, containers healthy, and https://ubag.polytronx.com/dashboard/sw.js
   returns 200. NOTE: the audit's breaking changes (AGENT_HANDOFF section
   below) are now LIVE on prod — batch endpoint requires Idempotency-Key
   (dashboard client already sends one), MFA gate widened, PAT/JIT hardened.
   The VPS box itself needs NO rebuild — CI builds images; the box only pulls.
3. **internal/plugins WASM host deletion** (last open deletion item). Kept
   only because server.go had concurrent uncommitted edits; those are now
   committed (7e9426b), so it is unblocked: remove internal/plugins/,
   the `Plugins` field from httpapi Config/Server, the `s.plugins.RunHooks`
   call sites in createJob/processBatchEntry (or keep the nil-guard hooks —
   they are no-ops today), and its tests. httpapi.Plugins is never
   constructed by serve.go.
4. **eslint + golangci-lint** (review item 50, deliberately deferred): no
   local toolchain to verify a gating config before CI runs it. Add
   .golangci.yml (conservative default linters) as a NON-blocking job first,
   triage findings, then flip to gating. Same for eslint on apps/dashboard
   (svelte-check + tsc gate today).
5. **MinIO integration test** — the MinIO service container was removed:
   quay.io/minio now requires authenticated pulls (docker hub minio/minio
   latest tags were deleted). TestMinIOArtifactStore stays env-gated/skipped.
   Fix = pin any reachable public MinIO-compatible image source and restore
   the service + UBAG_TEST_MINIO_* envs in ci.yml.
6. **eslint-scale doc ledgers**: IMPLEMENTATION_COVERAGE.md is recounted
   (45 REST + 286 scenarios); coverage gate stays at 50% with ADR-0014's 80%
   as documented long-term target — decide whether to raise in steps.
7. **Dashboard linux baselines**: bot commits them on dashboard-source
   changes (workflow dashboard-baselines.yml, dispatchable via API or
   workflow_dispatch); 4 routes (conversations, security, administration,
   antigravity) still have no chromium-linux baseline by design (see
   MISSING_LINUX_BASELINES in dashboard.spec.ts) — generate them inside the
   noble image to close the gap.

Everything else from the review is DONE and verified: WS-1 authz/authn
(batch gate, JIT/PAT hardening, SSO parity, MFA everywhere, gRPC parity,
worker option allowlist), WS-2 bounding (7 bodies, audit export cap, zero
err.Error() leaks, workflow idempotency), WS-3 pagination/CAS/upserts,
WS-4 migrations (authoritative checksums, transactional+locked, sqlite
reconciliation 0009–0013, outbox DDL 0017, audit WORM 0018, pool caps),
WS-5 performance (spool sweeper, per-artifact upload gate, metrics cache,
server timeouts, bounded memory stores, Link CAS, lease notification,
rate-limit fail-closed), WS-6 lifecycle (SSE Flush+deadline+heartbeat,
drain, daemon Close, real worker clock, SLO labels), WS-7 CI (service
containers, wired gates, pinned tools, new tests for templates/cmd/ubag/
chat-reaper), WS-8 contract (41 documented ops, bidirectional gate,
rbac.ts↔authz.go cross-check, doc corrections).

**Deployment pipeline** (from the same session): gateway-image.yml builds
gateway+dashboard on every main push, deploys pull-based via the
forced-command key (deploy-gateway sha-<sha> / deploy-dashboard with the
dist tarball over stdin — ci-deploy.sh swaps apps/dashboard/dist and
recreates nginx-dashboard to re-bind the inode). e2e-live's daily cron is
gated behind the LIVE_E2E_ENABLED repo variable (no staging exists).

## Breaking HTTP contract changes (2026-09-27/28 architecture audit)

The 2026-09-27/28 audit changeset (commits 23fd299…fb8667b, "secure by
default" per the review's scope decision) changed gateway behavior. OET
facade callers are UNAFFECTED (no facade route changed); the deltas apply
to direct gateway API callers:

1. **POST /v1/jobs/batch** — now requires the `job:create` permission (was
   unauthenticated to any role) and a caller `Idempotency-Key` header (16–128
   chars, or an `idempotency_key` field on the batch body). Entries without an
   explicit key derive `<batch key>-<index>`; a batch whose queue-depth check
   fails is rejected whole (429) before any entry is created. Backpressure now
   counts only queued+assigned jobs (terminal spool history excluded).
2. **MFA gate widened** — when MFA is enabled on the gateway, these actions
   require a completed `POST /v1/mfa/verify` for EVERY principal type
   (previously only SSO sessions): `secret:rotate`, `data:erase`,
   `auth:pat:issue`, `role:manage`, `region:manage`, `data:export`,
   `rate_limit:manage`. App-secret callers verify once (the marker is keyed
   on the caller's token, in-memory, 24 h TTL). With MFA disabled, no change.
3. **POST /v1/admin/elevation** — unknown roles are 400; `ttl_seconds` >
   86400 is 400. **POST /v1/admin/elevation/{id}/approve** — 403 unless the
   approver's role priority ≥ the grant's role priority.
4. **POST /v1/auth/pat** — unknown roles 400; negative (never-expiring)
   `ttl_seconds` is 403/400 for non-superadmin callers; > 1 year is 400. New
   route **POST /v1/auth/pat/{id}/revoke** (204; tenant-scoped 404).
5. **POST /v1/sso/oidc/callback** now requires authentication (the browser
   GET redirect flow stays exempt) — parity with SAML ACS.
6. **POST /v1/cache/invalidate** — now requires `rate_limit:manage` (was
   `job:read`, i.e. any viewer could purge the cache).
7. **Over-size bodies** on /v1/jobs/batch, /v1/cache/invalidate,
   /v1/auth/pat, /v1/templates/{id}/render, /v1/antigravity/* mutations now
   return 413 UBAG-VALIDATION-BODY-TOO-LARGE-001 (previously unbounded or a
   confusing 400). POST /v1/audit/export: limit defaults to 1000, > 10000 is
   400. Several 4xx/5xx error bodies no longer contain raw err.Error() text
   (stable messages; status classes unchanged).
8. **Worker option validation** — job options `user_data_dir` /
   `profile_dir` / `profile_path` must be non-absolute, traversal-free paths
   inside the worker profile root; `headless` must be a real boolean.
   Violations are 400 UBAG-VALIDATION-JOB-PAYLOAD-SAFETY-001 at create time.
9. **Rate limiting** — admin routes now share an `admin:manage` bucket
   (previously the generic unmatched-POST bucket), `job:retry` has its own
   policy, and limiter backend errors fail CLOSED (503
   UBAG-RATE-LIMITER-UNAVAILABLE-001) instead of open.
10. **GET /v1/metrics** is still unauthenticated by design but its payload
    is cached 5 s (ETag/304) and job-count scans are bounded at 10 000.

Operational deltas for deployments: migrations now run in a single
transaction behind an advisory lock (entrypoint + compose + Go runner);
postgres pool defaults capped at 20/5 connections; spool retention defaults
to 7-day TTL / 10 000 files (UBAG_SPOOL_RETENTION_TTL_SECONDS /
UBAG_SPOOL_RETENTION_MAX, 0 disables); shutdown grace
UBAG_SHUTDOWN_GRACE_SECONDS (25 s). New Postgres migrations 0014–0018 and
SQLite 0009–0013 apply on next boot; the outbox table (0017) is provisioned
for the (still optional) durable-dispatch path. CI now runs the 20
env-gated integration tests against real postgres:16/NATS/MinIO containers.

## Production on `cf8de88` (2026-09-13, live-verified)

Primary `185.252.233.186` runs
`UBAG_BUILD_COMMIT=cf8de88371e8329373e1fd7ccb3f09b90105f564` (strict
default build `0cc04e2` + docs/OpenAPI deltas). The primary session
staged the tarball + re-verified independently: ready
true, containers healthy, htpasswd 644, 0 panics, backup
`/opt/docker/ubag-sync-backups/ubag-pre-cf8de88-20260913T2131Z` on the
primary.

## vps2 retired (2026-10-01)

The second production box `213.163.201.37` (`upcloud-prod`, Ubag2) is
retired and no longer part of the platform. All vps2 reference material
(`docker-compose.vps2.yml`, `deploy/vps2/`) was removed from the repo;
production is the single primary box `185.252.233.186`.

## Primary on latest main `cf8de88` (2026-09-13, live-verified)

Primary `185.252.233.186` re-synced to main HEAD `cf8de88` — docs-only
delta over its prior `efd13d2` build (strict-by-default `0cc04e2` code).
Tracked-only tarball extracted over
`/opt/docker/ubag` (env.local/.htpasswd/.oet-pat.json/DBs/dist
untouched); gateway + chat-reaper rebuilt and recreated (browser
recreated too, profile volume kept). Verified:
`UBAG_BUILD_COMMIT=cf8de88371e8…`, `/v1/ready` fully true, 4/4
containers healthy, 0 panics, mock smoke `job_000000000422` exact token
`UBAG-CF8DE88-PRIMARY-OK`. Rollback: prior gateway image (from
`efd13d2`) + `/opt/docker/ubag-sync-backups/ubag-pre-cf8de88-20260913T2131Z`.

## Facade strict-by-default (2026-09-13, live on both boxes)

`ubag_strict` now defaults to STRICT (`0cc04e2`, CI green, YAML fix
`efd13d2`): the gateway injects the `_enabled:false` best-effort marker only
on explicit `ubag_strict:false`. Without the marker the worker enforces the
operator's model/reasoning settings on-page — explicit `model_settings` or
the per-provider selector defaults — fail-closed (`selector_drift_detected`
blocks the job on menu drift). Deployed on primary `185.252.233.186`
(image `efd13d2`, ready true, mock probes `job_...419/420`).
If OET calls start failing with `selector_drift_detected`, that is this
change working as mandated — fix the selectors for the drifted target, do
NOT flip the default back.

## Production performance state (2026-09-10 perf program, live)

UBAG `b9110ae` is live on VPS `185.252.233.186`
(`UBAG_BUILD_COMMIT=b9110ae4bb254e2f5b0e4d4bccfd21c412d32066`, `/v1/ready`
fully true, gateway/chat-reaper/browser healthy, 31/31 jobs completed today).
Branch `fix/oet-facade-timeout` was fast-forwarded into `main` on top of
`1b20fc7`; main HEAD differs from production only by docs commits. Exact-SHA
CI run `34498628459` on `51df379` completed successfully (all 8 jobs). Four
code commits: `53ddf45` reaps tabs leaked by a SIGKILLed daemon (the OOM /
tab-leak incident that broke every provider); `623c74a` races selector
candidates against one deadline with a visible-only filter, one-shot
`_fresh_chat` (no duplicate New-chat click), provider-signalled completion
(Stop control seen then gone + 0.75 s no-growth grace, 4 s settle kept as
fallback), warm tab reload only every 10 jobs, bridge screencast only while a
dashboard is connected, Chrome background services disabled, browser starts on
`about:blank`; `dff6115` disables `PreloadTopChromeWebUI` (132 MB omnibox
WebUI renderer gone); `b9110ae` orders duplicate matches by an in-page
hit-target check so ChatGPT's covered New-chat icon no longer burns a 1.5 s
click timeout, and `clear_attachment_state` is one `evaluate_all` round trip
(was five 130 ms `set_input_files([])` calls).

Measured wall-clock (OET pattern, `max_tokens` 16, `_enabled:false`), baseline
-> cold / warm after (cold = first job after a provider switch or restart):
duckai 12.6 -> 8.9 / 5.4-6.1 s; chatgpt 34.3 -> 20.9-26.9 / 12.4-14.2 s;
gemini 40.8 -> 17.2-24.9 / 7.1-7.6 s; deepseek 16.1 -> 12.4 / 9.1 s
(`job_000000000348`-`...368`, all completed with the exact expected text).
Browser idle cgroup 854.6 -> 196.9 MiB (~355-560 MiB with a provider page);
gateway idle ~6 MiB, ~140 MiB with the warm daemon. Memory limits unchanged
(browser 1900m, gateway 1300m). Worker overhead assigned->Send on warm ChatGPT
is ~2.7 s (was ~6.1 s); the rest is ChatGPT itself (~7 s to text + Stop button
lingering 3-4 s). Rollback: `ubag/gateway:rollback-before-b9110ae`
(= 623c74a+dff6115), `ubag/gateway:rollback-before-623c74a` +
`ubag/vps-browser:rollback-before-623c74a` (pre-perf),
`ubag/gateway:rollback-before-53ddf45`; tree backups
`/opt/docker/ubag-sync-backups/ubag-pre-{53ddf45-20260910T121422Z,623c74a-20260910T135623Z,b9110ae}`.
Retag to `:vps-local` / `:local`, restore the tree, `up -d gateway chat-reaper
browser`. Full evidence in `PROGRESS.md` top section.

**Deploy notes for next agent (learned this session):**
- New worker env knobs (allowlisted in `minimalWorkerEnv()`):
  `UBAG_REASONING_SETTLE_S` (4.0), `UBAG_INDICATOR_GONE_GRACE_S` (0.75),
  `UBAG_WARM_RELOAD_EVERY` (10); compose `UBAG_BROWSER_START_URL` (default
  `about:blank`). None are set in env.local - defaults are what is live.
- `up -d --build gateway chat-reaper` ALSO rebuilds and recreates the browser
  container (compose dependency): Chrome restarts, the warm page is lost, logins
  survive in the `browser_profile` volume. Expect one cold job per provider.
- Never touch the anti-detection flags in `deploy/vps/browser/entrypoint.sh`
  (headed Chrome on Xvfb, UA, GL, `--disable-blink-features=AutomationControlled`,
  no `--enable-automation`); only `--disable-features` entries were added.
  Chrome feature names live in the binary as `kFeatureName`. Keep LF endings.
- `gateway_job_events` timestamps are all written at completion - for
  per-phase timing attach an observer via CDP (`connect_over_cdp` to
  `http://172.28.0.10:9223`, worker page listed in
  `/var/lib/ubag/chat-ledger/open-pages.run_worker_daemon.py.json`).
- Deliberately kept (measured, not worth it): 900 ms post-New-chat settle (SPA
  route safety), Send-button click over Enter (multi-line prompts), 4 s settle
  fallback while a Stop indicator persists; queue poll already 75 ms.
  `job_000000000039` is a permanently stale `queued` row - ignore it.
- Reliable SSH from this workstation: write the script locally, then
  `Get-Content x.sh -Raw | ssh -o BatchMode=yes vps "tr -d '\r' | bash -s"`
  (env via `ssh vps "tr -d '\r' | PROVIDER=x bash -s"`); nested double quotes
  inside the ssh string break. Long builds: `nohup bash -lc '...' > /tmp/log
  2>&1 < /dev/null &` then poll the log from a separate ssh call.
- Worker tests: `apps/worker` -> `python -m pytest -q` (265 pass). Go:
  `apps/gateway` -> `gofmt -l`, `go vet`, `go test ./internal/executor/`.
- Worktree `D:\Projects\UBAG-fix-oet-facade-timeout` is merged; remove with
  `git worktree remove` when convenient.

## Production integration state (2026-09-10 nonce-aware OET probes, live)

UBAG main `1b20fc7` is live on VPS `185.252.233.186`: the OpenAI facade now
decodes nonempty `ubag_nonce` values into its v2 idempotency fingerprint while
preserving the exact legacy no-nonce key. Exact-SHA CI runs `34441400511` and
`34441399956` completed successfully. The immutable release archive was
28,272,396 bytes with SHA-256
`99ed21ad2dda3254972eab941e015f6febc381e6fdadc89317111302078b79de`.

Production runs image `sha256:ebac5a0901f467c4df28b4dba0581557b4121d563ead100ae2df6a68968da57c`,
`UBAG_BUILD_COMMIT=1b20fc70633a6bf08751ea9cfa7367c0207ad0fd`, and
`UBAG_FACADE_MAX_WAIT_MS=240000`; gateway and chat-reaper have zero restarts.
Browser/dashboard container IDs were unchanged, the browser profile volume
remains present, and PAT/htpasswd hashes match the root-only rollback backup
`/opt/docker/ubag-sync-backups/ubag-pre-1b20fc7-20260910T053709Z` (old image
`sha256:771d5acacc61afdea0eaa600457e378fa398434575309796f8cef5e594b616f5`).

Live nonce proof: same nonce replayed `job_000000000329`; a changed nonce made
fresh `job_000000000330`. The facade listed 40 models and all five OET targets.
Public OET matrix on unchanged OET SHA `a04b8674`: mock `...331` (333 ms),
ChatGPT Sol+Medium `...332` (88,210 ms), DeepSeek Instant `...334` (34,556
ms), and DuckAI Luna `...335` (42,526 ms) all returned expected output.
Gemini 3.8 Flash `...333` completed/routed correctly but returned `pong`; a
fresh retry `...336` returned exact `OK` in 63,845 ms. OET web/API/database
and all UBAG services remained healthy after the matrix. Full evidence is at
the top of `PROGRESS.md`.

## Production integration state (2026-09-08 marker+pins + cancel fix, live)

UBAG main `c22d3c9` live on VPS `185.252.233.186` (`/v1/ready` fully
true, image `c3835192a7829`): facade best-effort marker merges with
model pins (was: pins silently wiped, every job ran unconfigured) +
spurious facade-cancel fixed (was: "job ended as cancelled" with the
provider blamed) + provider-config env passthrough to worker. Live
proof: `job_000000000311` (deepseek Instant), `job_000000000312`
(ChatGPT Sol+Medium), `job_000000000313` (gemini) — all COMPLETED
exact tokens. Rollback: `ubag-sync-backups/ubag-pre-10bf7f4-20260908`.
Full evidence in `PROGRESS.md` top section.

## Production integration state (2026-09-08 dashboard blank-page fixed)

`https://ubag.polytronx.com/dashboard/` renders again (Basic Auth
admin/admin): the served bundle was built with `base: ""`, so all assets
404'd under `/dashboard/`; rebuilt with `UBAG_BASE_PATH=/dashboard`,
synced to `/opt/docker/ubag/apps/dashboard/dist` (backup at
`dist.bak.blankfix`), nginx-dashboard recreated healthy. Verified: entry,
layout, chunk, CSS all 200; /dashboard/jobs serves; /v1/jobs live data.
Rollback: swap `dist.bak.blankfix` back + recreate nginx-dashboard. Full
evidence in `PROGRESS.md` top section.

## Production integration state (2026-09-08 Sol+Medium composite, live)

UBAG main `cc54d27` (ci success) live on VPS `185.252.233.186` (`/v1/ready`
fully true): versioned full-fingerprint idempotency (v2 — model,
model_settings, messages, sampling hints, format, strict, attachments) +
`chatgpt_web|GPT-5.6 Sol + Medium` composite binding both settings at once.
Live proof `job_000000000303` COMPLETED exact token, merged options, retest
REPLAYED same job (no CONFLICT). OET `5e1c37a7` (deploy SUCCESS, blue slots,
health green, repo PRIVATE): ONE curated ChatGPT pick with recommended
label, per-probe ubag_nonce, allowlist excludes composite by design.
Rollback: prior image + env.local backup. Full evidence in `PROGRESS.md`.

## Production integration state (2026-09-07 all-providers E2E + drift-proof)

UBAG main `401bba7` (ci success ×2) live on VPS `185.252.233.186`
(`/v1/ready` fully true, 0 panics): single-source models list (thinking
levels addressable, toggles bare-target-only), gemini default 3.8 Flash +
thinking OFF, facade best-effort picker config (drift skips, never fails;
`ubag_strict:true` for fail-closed evals). Live proof: deepseek ✓, duckai
✓, gemini ✓ (exact tokens `…284/290/295`), chatgpt ✓ after merge fix
(`…297` with merged `{"_enabled":false,"model":"..."}`). OET main
`bb991e60` (Build & Deploy SUCCESS): catalog sync (allowlist + dropdown +
seeder refresh + tests). Mistral/perplexity = operator manual
logins (safe-mode, NOT bugs). Rollback: prior image + env.local backup.
Full evidence in `PROGRESS.md` top section.

## Production integration state (2026-09-07 Group E closed end-to-end)

UBAG main `7f030ed` (ci success) live on VPS `185.252.233.186` (`/v1/ready`
fully true, 0 panics): facade now serves chat + `ubag_attachments`,
`POST /v1/openai/audio/transcriptions`, `POST /v1/openai/embeddings`
(deterministic hash vectors, NOT semantic), and `response_format`
json coercion. OET main `385f791b` (Build & Deploy SUCCESS, all containers
on that SHA, site + api green): registry divert, ResponseFormatJson plumb,
route-aware listening call sites, embedding/exemplar wiring, Group E
unlocked as toggleable board rows (65 toggles), `ubag` row ACTIVE + keyed
with LastTestStatus ok, zero ubag routes (all OFF). Smoke: `…276/278/279`.
Rollback: prior images + env.local backup (unchanged). Full evidence in
`PROGRESS.md` top section. Remaining: admin board toggles A→E + scoring
parallel-eval window.

## Production integration state (2026-09-07 facade attachments live)

VPS `185.252.233.186` gateway serves the OpenAI facade WITH `ubag_attachments`
(rebuilt from main `d6706c1`, `/v1/ready` fully true, 0 panics): file
attachments (PDF/image/audio/video/voice) now flow through facade calls —
declare in the body, 202-held `ubag_job_id`, PUT each key to
`/v1/jobs/{id}/artifacts/{key}`, poll/replay resolves the completion.
Per-target manifest policy enforced (ChatGPT/Gemini/Mistral/Perplexity
full file kinds; DeepSeek docs+images; Duck.ai PDF+images; `mock` rejects).
Deploy smoke `job_000000000269` (text 200) + `job_000000000270` (202-held).
Group E status: OCR/transcription/summarise-JSON now servable via this shape
(OET-side routing still needed); Whisper-ASR,
embeddings, strict-JSON are OET-backend integration points, not UBAG gaps.
Rollback: prior image + env.local backup (unchanged). Full evidence in
`PROGRESS.md` top section.

## Production integration state (2026-09-07 OpenAI facade for OET)

VPS `185.252.233.186` gateway now serves the OpenAI facade live:
`POST /v1/openai/chat/completions` + `GET /v1/openai/models` (29 models),
commit `d6603ff`, smoke `job_000000000259` (mock, exact token, PAT-scoped
`tenant_oet`/`oet-platform`). `UBAG_APP_SECRET` rotated (backup
`deploy/vps/env.local.pre-oet-facade-20260907T070218Z`); PAT auth enabled;
OET PAT (service, no expiry) at root-only
`/opt/docker/ubag/deploy/vps/.oet-pat.json` — hand that value to the OET
operator for the `ubag` provider row (never print it). Gateway joined
`oetwebsite_internal`; `oet-api` reaches it privately (verified). Rollback:
prior image + env.local backup. Full evidence in `PROGRESS.md` top section.

**Deploy notes for next agent (learned this session):**
- `deploy/vps/env.local` vars reach containers ONLY if listed in the
  service `environment:` block (interpolation ≠ injection) — PAT was
  silently off until the compose passthrough was added.
- **SSH quote stripping:** `"` and `'` characters are stripped from ssh
  command strings in transit from this workstation. Write remote scripts
  with the Write tool, base64 them, `echo <b64> | base64 -d > file` on the
  VPS. Keep remote one-liners quoteless (`grep ^KEY= file | cut -d= -f2-`
  needs no quotes). Never print secrets: read them into remote-only shell
  vars, `unset` after, and save issued tokens straight to root-only files
  (print metadata only). Helpers used: `/tmp/ubag_smoke.py`
  (authed GET/POST via SMOKE_TOKEN env), `/tmp/ubag_pat_issue.py`,
  `/tmp/ubag_facade_smoke.py`, `/tmp/ubag_job_scope.py` (all in /tmp =
  ephemeral; recreate via base64 after a reboot).
- Authenticated probing from containers: gateway image has python3 but
  minimal shell tooling; `ubag-nginx-dashboard` (alpine) is on ubag-private
  for headerless checks; use the python helpers (via `docker exec -i -e
  SMOKE_TOKEN=$S ... python3 -`) for authed calls.
- No Go toolchain on this laptop: Go verification is CI (`go vet`, `go
  test -race`, `gofmt -l` all enforced). A local
  `gofmt_audit.mjs`-style struct-tag/const-alignment check caught real
  misalignments pre-push — re-run an equivalent check on any new Go file.
- Sync method stands: `git archive HEAD -o x.tar`, scp, extract in
  `/opt/docker/ubag` (tracked-only; env.local/.htpasswd/DBs/`.oet-pat.json`
  untouched). Commit first — never ship uncommitted work.
- OET repo work (seeder/board/guard/docs, uncommitted in
  `D:\Projects\OET with Dr Hesham\Web App`) still needs the OET ship-it
  flow + `UBAG_OET_PAT`/admin PAT paste + board enablement. OET guard
  change (`OET_INTERNAL_AI_HOSTS`) is REQUIRED for any UBAG call.

## Production performance state (2026-09-07 perf program)

VPS `185.252.233.186` now runs poll **75 ms** (`UBAG_WORKER_POLL_INTERVAL_MS=75`,
was 150), daemon ON (live warm reuse; mock stays per-job by design),
`UBAG_WORKER_MAX_RUNTIME_MS=1500000`, gateway 1 CPU/1300m. Deployed commits
`568941a`+`d6412ff` (gateway code == main HEAD; dashboard dist = main HEAD
with Group B). Image `ubag/gateway:vps-local` 540MB (was 733MB).

Single-sample event-derived timings on mock jobs `job_000000000256-258`
(all completed, exact token verified): queue (queued→assigned) **54ms**
(was 122ms p50), worker (assigned→completed) ~150ms, true E2E ~250ms
(was 491ms p50). SQL WaitEvents 300→50ms. Idle gateway 0.88% CPU / 12.9MiB.
Dashboard initial JS 100KB (was ~330KB shared); `tools/check-weight.mjs`
enforces budgets. 0 errors/panics in 20m post-deploy. Rollback: prior image +
`/opt/docker/ubag/deploy/vps/env.local.pre-perf-20260907` on VPS.

**Action required (operator): rotate `UBAG_APP_SECRET`** — an `sh -x` debug
run during the perf session echoed it into the agent transcript. Never left
VPS/transcript, but rotate in `deploy/vps/env.local` + platform copy.

**Deploy notes for next agent:** sync via tarball file (`git archive HEAD -o
x.tar`, scp, extract) — streaming `git archive | ssh tar -x` fails on VPS
tar. Build dashboard dist in an isolated `git worktree` so uncommitted work
never ships. Never `git add -A`: a parallel session shares this checkout —
stage explicit paths only, and never touch files it is editing
(jobs/webhooks/layout dashboard pages during Group B). New gateway env knob:
none (poll change is env.local-only). Smoke via container-side
`docker exec ubag-vps-gateway-1 wget` (host :8080 belongs to another project);
canonical create envelope needs `client.sdk.{name,version}`.

## Production performance state (2026-08-10)

Production VPS `185.252.233.186` retains a 150 ms file-spool worker poll and a
1500-second worker ceiling. In matched 20-sample deterministic mock runs, E2E
p50/p95 improved from **841.8/1145.8 ms** to **495.8/628.8 ms**, while queue
p50/p95 improved from **469/504.8 ms** to **122/143.8 ms**. Queue uses persisted
`queued -> assigned`; worker processing uses `assigned -> completed`. Idle
gateway CPU remained low, generally 0.4-0.8% after startup. The benchmark runner is
`tools/benchmark/run.mjs`; it is mock-only, sanitized, HTTPS-gated for remote
targets, and covered by 16 tests.

The gateway now receives one full CPU on this six-vCPU host. In an immediate
matched production check, moving from 0.6 to 1.0 core reduced E2E p50/p95 from
**648.0/979.3 ms** to **491.2/716.0 ms** and worker p50/p95 from
**497.0/794.9 ms** to **332.0/603.4 ms**.

Warm-browser isolation now keys reuse through canonical live-engine
normalization (trusted gateway `tenant_id`, target, resolved profile), rejects
closed/unresponsive pages and any non-successful prior job, and emits an
explicit terminal failure before a hard deadline exit. Provider
setting failures with visible sign-in UI report `manual_login_required`.
Gateway metrics now observe real queue, worker, ingestion, and terminal
end-to-end durations with bounded privacy-safe labels.

Production is healthy at commit `13dbdfa` on image
`sha256:cd17408f54bf164cee834b5455051f3538a225a8a4d1fb34e5f14ab06fbd6127`.
ChatGPT's current picker requires **Advanced -> Model / Effort**; selector
version `2026-08-10-advanced-model-menu` follows that path. Exact-token job
`job_000000000241` completed in 52.6 seconds with an exact match after focused
live-adapter/provider-config tests passed 46/46. Roll back with
`ubag/gateway:rollback-before-13dbdfa` if needed.

Gemini's selectors are valid, but its persistent production Chrome profile is
currently signed out; a human must log in before `3.6 Flash` can run again.
Safe-mode forbids automated login, credential storage, and CAPTCHA solving.

## Production Jobs page response-shape fix (2026-07-24)

Production `/v1/jobs?limit=20` was healthy (HTTP 200 in 9 ms), but canonical
list rows use `job_id` and `metadata.command_type`. The dashboard expected
`id`/top-level `command_type`; `job.id.slice(...)` threw during Svelte's render
flush and left the old `Loading...` node visible. A shared normalizer now maps
canonical and legacy shapes, rejects malformed rows, and is used by Jobs,
Overview, and Failed/DLQ. Focused dashboard validation: 25 Vitest tests passed;
Svelte diagnostics reported 0 errors and 0 warnings.

## Dashboard overview loading fix (2026-07-24)

The overview no longer performs duplicate concurrent jobs-list requests for its
metric cards and Recent Activity. Both sections reuse one collection response,
and the shared dashboard gateway client aborts stalled JSON and multipart
requests after 15 seconds with an actionable timeout error. Focused evidence:
22 dashboard tests passed, `svelte-check` returned 0 errors/0 warnings, and the
targeted dashboard build completed. No broad suite or CI ran.

## Current release state (2026-07-24)

Multi-file attachments and the warm-browser speed path are complete on `main`,
pushed, and deployed to production. The current gateway image is
`sha256:c6fdbaed65986850e9dc1374c95865acc6869b26edb6916da56b5def3493c62f`;
gateway/browser/dashboard are healthy and readiness is fully true. Production
source is `/opt/docker/ubag`; rollback assets are in
`/opt/docker/ubag-sync-backups/attachments-bf54c19-20260724`.

Live acceptance evidence: text `job_000000000032`; legacy ChatGPT audio
`job_000000000034`; ChatGPT multipart + warm reuse `job_000000000036` and
`job_000000000037`; DeepSeek document key-reference `job_000000000041`; Gemini
multipart document+WAV `job_000000000043`. Every successful provider job returned
its unique exact token. DeepSeek Web currently accepts documents/images only:
its live UI removes uploads in Expert mode and silently drops WAV/MP3-class
audio even in Instant. UBAG selects Instant for supported DeepSeek attachment
jobs and rejects audio/voice/video at create with
`UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001`.

Warm daemon invariants: six live targets use the daemon; mock/generic fall back
to the process runner; only one Sync Playwright manager may be alive in the
daemon thread; same-key jobs retain warm reuse; provider/profile changes close
the prior driver. Queue JSON treats omitted and empty optional objects as
equivalent but still poisons genuinely tampered envelopes.

Focused checks and responsive/build evidence are recorded at the top of
`PROGRESS.md`. No broad suite or CI was run, by explicit project/user instruction.
The only local untracked path is `.serena/`; preserve it.

## Attachment clients, worker, and dashboard completion (2026-07-23)

The non-gateway attachment implementation is complete on `feat/multi-file-attachments`: the worker rejects unsafe/duplicate manifest keys, count drift, and invalid or mismatched kind/MIME metadata; emits ordered attachment keys and kinds; and clears file-input state between warm-daemon jobs. TypeScript and Go SDKs expose compatibility aliases plus shared limits, and the CLI accepts one repeatable `--attach path[:kind]` flag per file with drive-letter-safe parsing and deterministic MIME validation. The Hallmark/NAJM dashboard jobs form provides an accessible drag/drop picker, ordered remove/clear list, 10-file/32 MiB-per-file/320 MiB-total client validation, all required component states, multipart submission, and horizontal-overflow protection. VPS worker daemon mode remains explicitly opt-in and false by default.

Focused green checks: worker **18 passed**; TypeScript SDK attachment **3 passed** after package build; focused Go SDK attachment tests passed; focused CLI repeatable-attachment test passed after package build; dashboard validation/client **17 passed**; dashboard check **0 errors / 0 warnings**; targeted dashboard build passed. Review follow-up proved the real loading-state priority (**3 state tests**), exercised two real engine attachment manifests through one reused mock driver without file-list inheritance (**9 warm-daemon tests**), and passed the single Chromium jobs-page overflow test at **320/375/414/768**. No broad suite or CI ran.

## Gateway attachment hardening (2026-07-23)

Post-review fixes are included: JSON and multipart create share `prepareCreateJob`, and multipart passes the prepared request through context so templates/plugins are not applied twice; unauthorized or template-invalid multipart requests fail before staging. Chunked bodies are bounded by the job-envelope limit before preflight and by policy bytes plus 8 KiB framing afterward. Rolled-back multipart writes do not increment stored-success metrics. Runtime entry-property/key/content-type bounds match the schema. Focused review regressions passed (15 parser, 5 HTTP; combined regression 22 parser and 36 HTTP), with targeted vet/diff-check clean.

Gateway attachment validation, held dispatch, multipart staging/idempotency, batch semantics, catalog policy, metrics, executor materialization, SQLite CAS, declared-byte immutability, and outbox crash-window recovery are focused-green. Verified from `apps/gateway`: 19 attachment-parser, 8 executor, 2 SQLite CAS, and 17 HTTP attachment/catalog tests; targeted vet and diff-check clean. No broad suite ran. Multi-process deployments still need a store-level immutable write primitive; direct non-outbox enqueue is not crash-replayed because enqueue is not guaranteed idempotent.

This is the resume point for any future agentic AI working in `D:\Projects\UBAG`.
Read this file first, then `PROGRESS.md`, then `IMPLEMENTATION_COVERAGE.md`.

## Current Repository State

- Working directory: `D:\Projects\UBAG`.
- Git is initialized on branch `main`, tracking `origin/main`.
- Preserve `AGENTS.md`, `design.md`, `.codex`, and all current workspace contents.
- Do not run `git reset`, `git clean`, or destructive checkout commands unless the user explicitly asks.

## Latest Slice: Multi-file attachments + faster pipeline (2026-07-23)

- Branch `feat/multi-file-attachments` generalizes the audio-only attachment path
  into first-class multi-file attachments (documents/audio/voice/images/video)
  end-to-end: contracts → gateway → worker → adapters → SDKs → CLI. See the top
  slice of `PROGRESS.md` for the full breakdown.
- Two ingestion flows: **key-reference** (`input.attachments` manifest → the job is
  held in `StatusCreated` until every artifact key is uploaded via
  `PUT /v1/jobs/{id}/artifacts/{key}`, then dispatches exactly once) and
  **multipart one-shot** (`POST /v1/jobs` as `multipart/form-data`: a `job` part +
  one file part per key; dispatches immediately). The dispatch gate is a
  `TransitionStatus` CAS (memory/sqlite/postgres); a TTL sweeper reaps jobs whose
  uploads never arrive.
- Per-adapter `attachments` policy in each manifest gates content-types
  (fail-closed: no policy ⇒ attachments rejected). `internal/attachments`
  (`DeclaredAttachments`) is the single source of truth used by create validation,
  the gate, and the worker-runner materialize.
- **Live DOM verification (2026-07-23, done):** inspected all three logged-in
  provider composers read-only via the Chrome extension. **ChatGPT** renders
  `input[type='file'][multiple]` at rest and **DeepSeek** renders a hidden
  `input[type='file']` after load; both match their selector baselines. The
  inspection did not submit a real attached job, so it proves DOM compatibility,
  not end-to-end attachment execution.
  **Gemini** renders NO file input at rest or after opening the menu; it is
  injected only when "Upload & tools" → "Upload files" fires the native file
  chooser. Fixed: added a `file_attach_trigger` click-path to Gemini's selectors +
  a Playwright `expect_file_chooser` interception path in the driver (mock-tested;
  needs one live Gemini worker run to confirm the real chooser).
- **BOM regression fixed on this branch:** all eight adapter manifests are now
  BOM-free, and the Go model-catalog loader defensively accepts BOM-prefixed
  bytes with focused regression coverage.
- Superseded by the 2026-07-24 release state above: merged, pushed, deployed,
  and live-verified.

## Latest Slice: Full tracked-file parity local ↔ GitHub ↔ VPS (2026-07-23)

- Local `main` is level with `origin/main` at `755772a` (0/0); only `.serena/` is untracked. GitHub ↔ local already exact.
- Production `/opt/docker/ubag` was audited against all 1,336 GitHub-tracked files via canonical git blob hashes. The only gap was the `.codex/skills/hallmark/` design skill (215 files) that earlier syncs excluded; every runtime/code/contract file already matched.
- Synced the 215 files with `git archive HEAD -- .codex/skills/hallmark` (additive only — no secrets, DBs, runtime files, or containers touched; no rebuild). Final re-audit: `MISSING=0 MISMATCH=0 EXTRA_TRACKED=0`. All four VPS containers stayed healthy.

## Latest Slice: Gemini 3.6 Standard + source synchronization (2026-07-23)

- Local `main` was fast-forwarded by 109 commits to GitHub `origin/main` at `9da31f5`; the full pre-sync dirty tree remains recoverable in `stash@{0}` (`codex-pre-sync-2026-07-23-local-and-gemini36`).
- Production `/opt/docker/ubag` was compared to GitHub source-only. Runtime logs, spool records, databases, generated binaries, `.htpasswd`, and `deploy/vps/env.local` were excluded. Production's worker engine/page driver match GitHub; only the Gemini selector policy was newer and eligible to promote.
- Gemini now enforces model `3.6 Flash` and treats Standard thinking as `Extended thinking = false`. The flattened Gemini picker persists these selections independently.
- Shared commit `acec1ed` was pushed to GitHub `main`, and all 1,121 tracked files were synchronized into production with zero missing files and zero hash mismatches.
- Production gateway image `sha256:dde174b3d9422bba95c4022c753171f7b0ae830a3617acec6a798875eec52559` is healthy. Final live job `job_000000000029` completed with exact output `UBAG_SYNCED_GEMINI_36_STANDARD_OK` and selector version `2026-07-23-gemini-3.6-standard`.

## Latest Slice: Orchestration Semantics (2026-07-16)

Per-request model/mode selection + conversation affinity landed across contracts, gateway, worker, and SDK/CLI/dashboard, inert by default behind `UBAG_CONVERSATIONS_ENABLED` (default false). See the 2026-07-16 section of `PROGRESS.md` for the full description and the design/plan under `docs/superpowers/`.

Key runtime facts for the next agent:

- New env flag `UBAG_CONVERSATIONS_ENABLED` (default false). When enabled, the store backend follows the existing `UBAG_GATEWAY_STORE` `storeKind` (memory/sqlite/postgres), exactly like alerts. Postgres requires applying `migrations/postgres/0010_conversations.sql` (readiness fails closed otherwise); SQLite self-bootstraps.
- New route `GET /v1/conversations` (`job:read`, nil-safe 501 when disabled).
- `job.model_settings` is a flat map keyed by each adapter's own `ProviderSetting.key`; the gateway validates it against the adapter manifest `model_catalog` and copies it into the worker envelope `options.provider_config`. Client-supplied `options.provider_config` is stripped at create time.
- Worker emits `conversation.thread_bound/_broken/_rebound` with a **flat** top-level `thread_ref` (chat URL only) — the gateway `WorkerConsumer` reads `data.thread_ref` non-recursively; keep any new emitter flat.
- Next roadmap slices (not started): provider expansion (Kimi/Minimax activation), automatic provider fallback/routing, mobile push alerting.
- Follow-ups: promote the dashboard `/conversations` page into the sidebar nav (requires updating the §24.2 17-page inventory + the e2e count); add typed `model_settings` to gRPC/proto if a typed non-HTTP surface is wanted.

Host note: bare `python` on the current Windows host resolves to a broken Store alias stub; use `C:\Users\Admin\AppData\Local\Python\bin\python.exe` with `PYTHONPATH="apps/worker;adapters/mock"`. The single gateway test `TestProcessWorkerRunnerRunsPythonWorkerFromGatewayEnvelope` fails only for this alias reason.

## Current Product Phase

The repository has completed the docs-first Milestone 0 baseline and the v0 edge foundation slice.

Current implemented or validateable scope:

- Astro Starlight documentation site under `apps/docs`.
- Root planning and tracking docs: `PRD.md`, `PROGRESS.md`, `IMPLEMENTATION_COVERAGE.md`, and this handoff.
- OpenAPI, shared JSON Schemas, Protobuf seed contracts, SDK fixtures, and contract checks.
- Conformance fixtures currently include 41 executable REST scenarios plus 272 named non-executable coverage scenarios.
- Go gateway with `/v1` health, readiness, version, metrics, jobs, tenant/app-scoped cross-job events, SSE, WebSocket upgrade guard, workflows, built-in template catalog/application, targets/adapters, apps, devices, webhooks, cache status, audit, cancel, retry, stable errors, idempotency, idempotent artifact mutations, paginated operator collections, and app-secret auth.
- Gateway-side executable payload safety checks, internal executor dispatch boundary, and optional embedded worker consumer/result ingestion for local file-spool and NATS JetStream leases.
- Opt-in Postgres gateway stores for jobs, events, worker-event dedupe keys, and idempotency records via `UBAG_GATEWAY_STORE=postgres`.
- Edge queue and SQLite/localfs-oriented storage contracts plus migrations and conformance checks; gateway runtime persistence is memory by default and Postgres/MinIO when configured.
- Python worker, deterministic mock adapter, safe-mode provider manifests, manual-session events, artifact policies, and secret-material rejection.
- Safe-mode adapter coverage for DeepSeek, ChatGPT, Gemini, Mistral, DuckAI, generic chat, generic form, and mock. (claude_web retired 2026-09-26; perplexity_web removed 2026-10-05.)
- TypeScript/JavaScript and Go SDK wave with generated operation-level contract-manifest freshness checks for system, job, job-event, artifact list/upload/download/delete, operator collection, webhook replay, workflow/template list, cache, apps/devices/audit, metrics, and stream entrypoint endpoints.
- TypeScript CLI with health/ready/version, diagnose, create/get/list/cancel/retry, event/artifact/operator/webhook/cache/metrics commands, SSE streaming, mock-run, and adapter-test coverage.
- Loopback sidecar with `/health`, `/v1/*` proxy, mutating-route idempotency generation including artifact PUT/DELETE, and public-binding guard.
- NAJM/Hallmark operator dashboard under `apps/dashboard`, wired to gateway APIs with local fixtures only for tests and empty/offline states. The dashboard consumes gateway-native browser topology fields, embeds only runtime-generated loopback noVNC URLs, renders template preview output from the gateway `rendered` field, and does not invent workflow DAGs when the list endpoint only returns metadata.
- Security/compliance contracts for app-secret auth, device tokens, RBAC/ABAC, audit redaction/chaining, webhook signing, and rate-limit decisions.
- Observability package with metric, event, log, health-probe, and smoke-check registries.
- Small deployment profile with Docker Compose, nginx-dashboard ingress, Postgres, Dragonfly, MinIO, Prometheus/Grafana, and optional NATS.
- NATS JetStream gateway dispatch and embedded durable worker consumption are implemented via `UBAG_EXECUTOR_MODE=nats`, `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, `UBAG_NATS_SUBJECT`, and the `UBAG_NATS_WORKER_*` settings.
- MinIO artifact storage is implemented via `UBAG_ARTIFACT_STORE=minio`, `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL`, with Postgres metadata in `migrations/postgres/0002_artifact_metadata.sql` when the gateway store is Postgres-backed.
- Signed webhook outbox delivery is implemented via per-job callback config, `UBAG_WEBHOOK_OUTBOX`, Postgres migration `migrations/postgres/0003_webhook_outbox.sql`, HMAC signing secrets from environment, strict callback URL policy, and an opt-in retry worker controlled by `UBAG_WEBHOOK_WORKER_ENABLED`.
- Built-in template catalog/runtime foundation is implemented in memory: `/v1/templates` lists built-ins, readiness verifies the template store, and job creation applies template defaults before payload policy validation, storage, idempotency hashing, and executor enqueue.
- The latest gateway completion sweep hardened secret-like payload key detection, replaced the `/v1/events` placeholder with real scoped event listing, added collection pagination/AuthZ, required idempotency for artifact PUT/DELETE replay, aligned the Mistral adapter catalog key as `mistral_lechat`, and added proto/OpenAPI/schema lint command coverage.
- The latest hardening pass closed repo-local audit gaps in template-default job creation, callback secret-reference handling, manual-session event data preservation, sidecar artifact idempotency, SDK/CLI endpoint parity, dashboard CSP/state coverage, small-profile public ingress guards, Postgres migration reruns, MinIO least-privilege bootstrap, nginx-dashboard ingress, gateway graceful shutdown, observability readiness/smoke probes, contract drift, and docs claim accuracy.
- The 2026-05-29 pass added a runtime SQLite/localfs persistence path and six enterprise leaf packages to the gateway, all code-complete and locally validated with green `go build`/`vet`/`test ./...` (gRPC + grpc-web were completed in a previous slice):
  - Runtime stores: `UBAG_GATEWAY_STORE=sqlite` (WAL, `busy_timeout`, `foreign_keys`, single-writer), `UBAG_ARTIFACT_STORE=localfs` with `UBAG_ARTIFACT_DIR`, and a SQLite webhook outbox mode.
  - `internal/ratelimit` (memory + SQLite + Postgres stores, policy resolver), `internal/responsecache` (memory + SQLite, never exposes cached payload values), `internal/workflow` (memory + SQLite multi-step runs with payload policy on every step input), `internal/sso` (stdlib OIDC RS256 + SAML verification, memory + SQLite config store), `internal/scim` (SCIM v2 Users/Groups CRUD+Patch, memory + SQLite, passwords never stored), and `internal/siem` (redacted audit/event export via File/HTTP/Syslog sinks with a non-blocking exporter).
  - `internal/httpapi` wiring is nil-safe/optional so unconfigured subsystems leave existing behavior unchanged; new routes and RBAC actions are `GET /v1/cache` (`job:read`) and `DELETE /v1/cache` (`rate_limit:manage`), `GET /v1/rate-limits` (`rate_limit:manage`), `GET/POST /v1/workflows` + `POST /v1/workflows/{id}/runs` + `GET /v1/workflows/runs/{id}` (`job:read`/`job:create`), `GET/PUT /v1/sso/config` (`role:manage`) + `POST /v1/sso/oidc/callback` + `POST /v1/sso/saml/acs` (verification, no RBAC), `/v1/scim/v2/Users[/{id}]` and `/v1/scim/v2/Groups[/{id}]` (`role:manage`), `GET/PUT /v1/siem/config` (`role:manage`) + `POST /v1/audit/export` (`data:export`), `POST /v1/webhooks/secret:rotate` (`secret:rotate`), and a `withRateLimit` middleware that is pass-through when disabled.
  - New env vars: `UBAG_RATE_LIMIT_ENABLED` (default false), `UBAG_CACHE_ENABLED` (default false), `UBAG_CACHE_TTL_MS`, `UBAG_SIEM_FILE_PATH`.
  - Independent review PASSED with no Critical/High findings; two hardening fixes were applied (cache purge returns `501` when disabled; SSO config `PUT` rejects OIDC without an Issuer and SAML without an IdP certificate).
  - SDK limitation: active first-class SDK support is TypeScript/JavaScript and Go only. Prior Rust, Python, Java, Kotlin, Ruby, PHP, C#, Swift, and Elixir SDK package trees are no longer part of active CI, docs, packaging, or release claims.

## Subagent Audit Closure

The initial v0 baseline closed six parallel review workstreams. Later implementation slices added further parallel reviews for Postgres gateway stores, NATS/MinIO, NATS worker consumption, signed webhook outbox delivery, and the 2026-05-24 completion sweep. The detailed evidence chain and subagent counts are tracked in `PROGRESS.md`; this handoff records only the current resume state.

## Latest Known Green Validation

After the 2026-06-17 TS+Go-only SDK completion pass, the following validation passed:

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

Docker Compose was not installed on this host, so `test:deployment` used its
static deployment checks after explicitly reporting the compose-render skip.
The supported SDK set is TypeScript/JavaScript (`@ubag/sdk`) and Go
(`github.com/ubag/ubag-go`) only.

After the 2026-06-18 dashboard-only completion pass, the following validation passed:

```powershell
cmd /c pnpm --filter @ubag/dashboard check
cmd /c pnpm --filter @ubag/dashboard test
cmd /c pnpm --filter @ubag/dashboard test:e2e
cmd /c pnpm test:dashboard
```

After the 2026-06-18 production live-browser activation, `ubag.polytronx.com`
uses the `live-browser` profile with a production `browser-topology-register`
service. The registrar idempotently upserts one Chromium instance and three
provider contexts/tabs (`chatgpt_web`, `gemini_web`, `deepseek_web`) for
`tenant_edge`, so Browser Sessions should survive restarts/redeploys without
manual database inserts. Production verification returned 1
`gateway_browser_instances` row, 3 `gateway_provider_contexts` rows, and 3
joined `gateway_browser_tabs` rows. Gemini and DeepSeek were operator-login
checked; ChatGPT remains manual-login pending. Do not read provider cookies,
storage state, credentials, or production secret files while validating this
flow.

After the 2026-06-18 production operator activation pass, production also runs
`browser-topology-sync` under the `live-browser` profile. It reruns the same
idempotent registration every `UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS` seconds, so
Browser Sessions should repopulate automatically after restarts without manual
DB inserts. The production dashboard bundle now includes:

- Jobs page submitter for `chatgpt_web`, `gemini_web`, and `deepseek_web` using
  the real `/v1/jobs` envelope.
- Correct job cancel/retry routes (`/v1/jobs/{id}/cancel`,
  `/v1/jobs/{id}/retry`).
- Workflows page create/run controls using `/v1/workflows` and
  `/v1/workflows/{id}/runs`.
- Workflows page ordered-chain mode that creates steps in this provider order:
  ChatGPT, Gemini, DeepSeek. It keeps single-provider mode available and shows
  live provider readiness from `/v1/browser/contexts`.

Production smoke evidence: `https://ubag.polytronx.com/dashboard/jobs/`,
`/dashboard/workflows/`, and `/dashboard/browser/` rendered in headless Chrome;
browser topology showed 1 instance, 3 contexts, 3 tabs; safe mock job
`job_000000000001` was accepted/queued; safe mock workflow
`wfd_6d78879ffd80099234a51848` ran successfully as
`wfr_e198f2fa93daa73b20f1a810` with `job_000000000002`. No failed jobs existed
at inspection time. A follow-up smoke confirmed the ordered workflow UI renders
the requested chain and provider readiness states. External `/v1/ready` is
intentionally blocked by nginx; use external `/v1/health` and internal container
healthchecks for readiness.

After the 2026-06-01 worker runtime orchestration integration (Option A, full), the following validation was green (all exit 0):

```powershell
node tools/run-go-tests.mjs apps/gateway        # all packages ok (executor re-ran with new topology tests)
node tools/run-python-worker-tests.mjs          # 143 tests (122 legacy + 21 new) + 5 + smoke, EXIT=0
cmd /c pnpm check
cmd /c pnpm test:v0
```

The live worker now optionally routes jobs through `LiveOrchestrator` (Fleet + per-(tenant,provider,identity) ChannelPool with persistent AIMD) and emits `browser.topology_reported` + `concurrency.cap_changed`; the gateway `WorkerConsumer` projects topology snapshots into its in-memory `topology.MemoryStore` (tenant-forced, storage-state redacted, poison-safe intercept). The integration is opt-in (`orchestrator=None` and a nil `Topology` ingestor keep the legacy path byte-identical), so all pre-existing tests stay green. The live real-browser provider path is still ToS-bound and cannot be CI-validated; all new wiring is validated via offline/mock drivers, fakes, and unit/structure tests only.

After the 2026-05-29 gateway runtime-stores and enterprise-surface pass, the following gateway validation was green on the Go 1.26 toolchain (all `apps/gateway` code-complete and locally validated):

```powershell
go build ./...
go vet ./...
go test ./...
cmd /c pnpm test:plugins
cmd /c pnpm test:adapter-registry
cmd /c pnpm test:v0:local
```

`test:plugins` reports 20/20 and `test:adapter-registry` reports 16/16. The new SQLite/localfs runtime stores and the `ratelimit`/`responsecache`/`workflow`/`sso`/`scim`/`siem` packages each ship with passing Go package tests; live multi-process durability and Postgres-native stores for the non-rate-limiter subsystems remain follow-ups.

After the 2026-05-25 continuation hardening pass, the following sequential validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:deployment
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git --no-pager diff --check
```

The following commands passed after the v0 implementation and subagent fix pass:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:deployment
cmd /c pnpm check:contracts
cmd /c pnpm check:sdk-freshness
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the 2026-05-24 hardening pass, the following validation passed:

```powershell
cmd /c pnpm check:docs-responsive
cmd /c pnpm test:dashboard
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

`cmd /c pnpm test:v0` passed after responsive verifiers were moved to OS-assigned local ports and page-readiness polling so concurrent or stale local preview servers no longer affect the checks.

`cmd /c pnpm test:v0` includes schema, edge-store, security, worker, sidecar, SDK, conformance, observability, CLI, dashboard, deployment, docs, responsive docs, and gateway Go tests.

SDK/conformance coverage currently validates 41 executable REST scenarios plus 272 named non-executable coverage scenarios, including executor dispatch, file-spool/NATS worker ingestion, and webhook outbox retry.

After this handoff documentation was added, the following validation passed again:

```powershell
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm test:docs
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the gateway executor dispatch slice, the following validation passed:

```powershell
cmd /c pnpm check:contracts
cmd /c pnpm test:observability
cmd /c pnpm test:gateway
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

After the worker consumer/result-ingestion slice, the focused validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:worker
cmd /c pnpm test:observability
```

The full post-ingestion validation passed after rerunning `test:v0` and `check` sequentially so the docs responsive server had exclusive port ownership:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm check:contracts
cmd /c pnpm test:conformance
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

After the Postgres gateway-store slice and review-blocker fix pass, the following validation passed:

```powershell
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:gateway
cmd /c pnpm check:contracts
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

Postgres integration tests are env-gated by `UBAG_TEST_POSTGRES_DSN` and skip without a live disposable database.

After the NATS JetStream executor and MinIO artifact-storage review-blocker fix pass, the following validation passed:

```powershell
cmd /c pnpm test:deployment
cmd /c pnpm check:contracts
cmd /c pnpm test:docs
cmd /c pnpm check:docs-responsive
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:sdk
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

NATS and MinIO live integration tests are env-gated by `UBAG_TEST_NATS_URL` and `UBAG_TEST_MINIO_ENDPOINT`.

After the NATS worker-consumer slice, the following validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:worker
cmd /c pnpm test:deployment
cmd /c pnpm test:conformance
cmd /c pnpm check:contracts
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

`cmd /c pnpm test:v0` and `cmd /c pnpm check` were rerun sequentially after an intentional parallel validation attempt caused responsive-check server port contention.

After the signed webhook outbox and retry-worker slice, the following validation passed:

```powershell
cmd /c pnpm test:gateway
cmd /c pnpm test:deployment
cmd /c pnpm test:conformance
cmd /c pnpm test:observability
cmd /c pnpm check:contracts
cmd /c pnpm test:docs
cmd /c pnpm test:v0
cmd /c pnpm check
cmd /c pnpm --package=@redocly/cli dlx redocly lint packages/openapi/openapi.yaml
git diff --check
```

Runtime worker ingestion state after this slice:

- Gateway executor mode defaults to `noop`.
- Optional local dispatch is `UBAG_EXECUTOR_MODE=file` with `UBAG_EXECUTOR_SPOOL_DIR` pointing at ignored runtime storage such as `var/executor-spool`.
- Optional durable dispatch is `UBAG_EXECUTOR_MODE=nats`; configure `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`. When `UBAG_WORKER_CONSUMER_ENABLED=true`, the embedded worker consumer leases a durable JetStream pull consumer configured by `UBAG_NATS_WORKER_DURABLE`, `UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`, `UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`. Env-gated NATS integration tests use `UBAG_TEST_NATS_URL`.
- Gateway rejects executable job payloads containing credentials, cookies, tokens, API keys, browser storage/session state, client-supplied noVNC URLs, private keys, MFA/TOTP material, or CAPTCHA-solving instructions before job storage or dispatch.
- File-spool dispatch writes gateway-stamped envelopes under `pending/`; the embedded worker consumer can atomically lease them under `leased/`, invoke the Python worker, ingest gateway-sequenced worker events/results into job history, and finalize under `done/`, `failed/`, or `cancelled/`.
- NATS dispatch publishes gateway-stamped envelopes to `<subject>.<jobID>` and cancellation notices to `<subject>.cancel.<jobID>`. The embedded NATS worker consumer filters `<subject>.*`, reconstructs execution envelopes from persisted jobs before invoking the Python worker, acknowledges only after durable terminal ingestion or synthetic retryable failure, nacks transient setup/store failures with delay, and terminates malformed or mismatched envelopes as poison messages.
- Enable embedded ingestion with `UBAG_WORKER_CONSUMER_ENABLED=true`, `UBAG_WORKER_PYTHON`, `UBAG_WORKER_SCRIPT`, `UBAG_WORKER_POLL_INTERVAL_MS`, and `UBAG_WORKER_MAX_RUNTIME_MS`.
- Gateway stores default to memory. Set `UBAG_GATEWAY_STORE=postgres` and `UBAG_POSTGRES_DSN` to persist gateway jobs, job events, worker-event dedupe keys, and idempotency records in Postgres. `migrations/postgres/0001_gateway_stores.sql` must be applied before `/v1/ready` can pass in Postgres mode. Readiness verifies `gateway_job_id_seq`, `gateway_jobs`, `gateway_job_events`, `gateway_job_worker_event_keys`, and `gateway_idempotency_records`. Set `UBAG_GATEWAY_STORE=sqlite` for a single-node SQLite runtime store (WAL, `busy_timeout`, `foreign_keys`, single-writer guard).
- Artifact storage defaults to memory. Set `UBAG_ARTIFACT_STORE=minio` with `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL` to use MinIO/S3-compatible object storage. Set `UBAG_ARTIFACT_STORE=localfs` with `UBAG_ARTIFACT_DIR` for a local-filesystem artifact store. If Postgres gateway stores are active, apply `migrations/postgres/0002_artifact_metadata.sql`; readiness verifies `artifact_metadata`. Artifact list/get requires `job:read`, upload requires `artifact:write`, delete requires `artifact:delete`, and cross-tenant artifact access is hidden as not found through the owning job lookup. Env-gated MinIO tests use `UBAG_TEST_MINIO_ENDPOINT`, `UBAG_TEST_MINIO_ACCESS_KEY`, and `UBAG_TEST_MINIO_SECRET_KEY`.
- Webhook delivery defaults to an in-memory outbox unless Postgres gateway stores are active. Set `UBAG_WEBHOOK_OUTBOX=postgres`, apply `migrations/postgres/0003_webhook_outbox.sql`, and enable `UBAG_WEBHOOK_WORKER_ENABLED=true` with `UBAG_WEBHOOK_SECRET` or per-secret environment variables for durable signed retries. A SQLite webhook outbox mode is also available for single-node deployments. Callback URLs require `callbacks.webhook_secret_id` when `callbacks.webhook_url` is set and are validated before job storage and before delivery; public hosts must match `UBAG_WEBHOOK_ALLOWED_HOSTS` unless `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true` is explicitly enabled after outbound SSRF review, and unsafe private/local URLs, userinfo, fragments, and secret-looking query keys are rejected unless explicit operator policy allows them. Replay requires an existing tenant/app-scoped delivery ID, idempotency key, and audit reason.
- API-facing job reads return `404` for out-of-scope tenant/app jobs to avoid cross-tenant job existence leaks.

Runtime probe evidence from the latest full run:

- Gateway health URL: `http://127.0.0.1:8080/v1/health`.
- Docs site URL: `http://127.0.0.1:4321/`.
- Dashboard URL: `http://127.0.0.1:4177/`.
- Health status: `ok`.
- API version: `2026-05-22`.
- Readiness status: `ready`.
- Created probe job: `job_000000000001`.
- Event count: `1`.
- SSE contained queued event: `true`.
- Metrics included HTTP request and SSE current gauges: `true`.
- Docs and dashboard returned HTTP 200 with expected titles.

## Resume Procedure

Start every continuation with this exact sequence:

```powershell
git status --short --branch
cmd /c pnpm install --frozen-lockfile
cmd /c pnpm test:v0
cmd /c pnpm check
git diff --check
```

Use `cmd /c pnpm ...` on Windows if PowerShell script policy blocks direct `pnpm` execution.

If you need local manual inspection, use these services:

```powershell
cmd /c pnpm docs:dev
cmd /c pnpm --filter @ubag/dashboard dev --host 127.0.0.1 --port 4177
```

For the gateway edge runtime:

```powershell
$env:UBAG_APP_SECRET="dev-secret"
$env:UBAG_API_VERSION="2026-05-22"
$env:UBAG_GATEWAY_ADDR="127.0.0.1:8080"
make dev-edge
```

## Critical Invariants

- API version is `2026-05-22`.
- App-secret auth binds to the configured tenant/app/role principal; do not trust caller-supplied actor headers as identity.
- Mutating routes require an `Idempotency-Key`; CLI/sidecar may auto-generate one when acting as local clients.
- Artifact PUT/DELETE are mutating routes and require an `Idempotency-Key`; PUT replay returns stored artifact metadata and DELETE replay returns `204`.
- Payloads must not include credentials, cookies, tokens, API keys, secrets, session/browser storage, or CAPTCHA-solving material, including compact key variants such as `apiKeyValue` and `client_secret_value`.
- Gateway-side payload safety checks must run before job storage or executor dispatch.
- Browser automation remains user-owned manual login through live browser/noVNC sessions.
- noVNC URLs must be generated by the runtime and stay loopback/operator-scoped; do not accept arbitrary noVNC URLs from job payloads.
- Safe mode is the default automation stance.
- Standard privacy mode is the current default; HIPAA/GDPR modes require later activation evidence.
- Caddy admin must remain localhost-bound.
- `deploy/small/small.ps1 -Action config` must render from `env.example` unless `-AllowSecretConfigOutput` is explicitly provided.
- Do not expose backing service ports publicly without an explicit firewall and deployment review.

## External Activation Items

These are not missing repository work; they require facts or services outside this checkout.

| Item | Required External Input |
| --- | --- |
| Live AI provider execution | User-owned accounts, manual browser login, active sessions, and provider-specific consent. |
| Small-profile runtime smoke | Docker Desktop Linux engine or an equivalent Docker host. |
| Production deployment | Host, DNS, TLS, secrets, firewall policy, and explicit operator approval. |
| Live webhook endpoint smoke | Real callback targets, outbound allowlist, shared signing secrets, and a disposable network path. |
| HIPAA/GDPR modes | Legal/compliance review, BAAs/DPAs where needed, deployed evidence, and operator policy choices. |
| Marketplace/app distribution | Publishing accounts, release credentials, and governance approval. |

## Next Coding Queue

Pick up from these implementation tracks after preserving the current green baseline:

1. Commit the current green baseline when the user approves.
2. Convert safe-mode provider stubs into live manual-session browser adapters after user-owned account/session requirements are available; acceptance requires manual-session consent, no credential/session/token storage, no CAPTCHA bypass, runtime-generated noVNC URLs only, adapter allowlisting, tenant/app scoping, audit events, and artifact redaction. The orchestration layer (`apps/worker/ubag_worker/orchestration`: topology/AIMD/pacer/channel-pool/bulkhead/scheduler) and cross-engine grid abstractions (`apps/worker/ubag_worker/live/engines.py`, `live/remote.py`) are now implemented and unit-tested as the ToS-safe substrate for this; the live adapter wiring remains the external-account-gated step.
3. DONE (v2.1): Gateway sessions are now minted from the verified SSO principal on `/v1/sso/oidc/callback` and `/v1/sso/saml/acs` (opaque crypto/rand token, SHA-256 at rest, HttpOnly cookie + JSON token, `POST /v1/sso/logout` revokes), bound to tenant/app/role, audited, with no credential/session storage.
4. Replace the pragmatic SAML check with exclusive XML-C14N signature verification before onboarding a production IdP. PARTIAL (v2.1): `internal/sso/canonicalize.go` applies exclusive XML-C14N (`xml-exc-c14n#`) before digest/signature verification and fails closed; harden against a real production IdP before claiming full conformance.
5. DONE (v2.1): Native Postgres stores added for response-cache, workflow, SSO, SCIM, SIEM, and webhook-secret subsystems (migrations `0005_enterprise_stores.sql`, `0006_audit_sessions.sql`); each fails fast via `Ready()`/`to_regclass` so Postgres deployments no longer silently fall back. Round-trip tests are env-gated on `UBAG_TEST_POSTGRES_DSN`.
6. DONE (v2.1): `POST /v1/audit/export` exports real Merkle-chained audit records from `internal/audit` (memory + SQLite + Postgres), accepts the SDK request body (`idempotency_key` ignored read field, optional `range.{from_sequence,to_sequence}` post-filter), and verifies the chain over the full result before windowing.
7. Continue hardening workflow/cache/template runtime beyond the current validated foundation; acceptance requires durable template authoring, richer workflow DAG/saga semantics, retention controls, privacy-mode cache bypasses, and expanded SDK/conformance coverage.
8. Keep SDK release work scoped to TypeScript/JavaScript (`@ubag/sdk`) and Go (`github.com/ubag/ubag-go`); do not reintroduce other SDK package trees without an explicit product decision.
9. Broaden TypeScript/JavaScript and Go SDK conformance beyond REST fixtures where runtime services exist, including event streaming and live binary artifact smoke before new transports are claimed.
10. DONE (v2.1): Worker-side `ConcurrencyRegistry.Report` is wired to AIMD cap-change events. The worker emits `concurrency.cap_changed` telemetry (`orchestration/telemetry.py`); the gateway intercepts it in the `WorkerConsumer` ingest loop and routes it to `topology.ConcurrencyRegistry.Report`, so `/v1/concurrency` reflects live worker-reported lane concurrency. Covered by gateway and worker unit tests.
11. Add CI after remote policy is known. The repository now has a baseline commit (`0364595`, v0 platform) and a v2.1 delta commit (`85d6eb0`); neither is pushed. Postgres round-trip tests can run in CI via `pnpm test:gateway:postgres` (needs `UBAG_TEST_POSTGRES_DSN`; see `docs/postgres-roundtrip-tests.md`).
12. Onboard real live providers using `live_web_template(...)` / `generic_live_web` and `apps/worker/ubag_worker/live/ONBOARDING.md`; activation still requires user-owned provider accounts and manual login.
13. DONE (2026-06-01): Worker runtime orchestration integration (Option A, full). `LiveSessionEngine` accepts an optional `LiveOrchestrator` (`apps/worker/ubag_worker/live/orchestrator.py`) that wires Fleet/ChannelPool/persistent-AIMD/topology into the live path and emits `browser.topology_reported` + `concurrency.cap_changed`; `create_default_driver` now honors `engine_spec_from_env()` via the pure `_resolve_launch_plan` helper. The gateway `WorkerConsumer` projects topology snapshots into its in-memory topology store (tenant-forced, storage-state redacted, nil-safe). Opt-in/backward-compatible. Covered by `apps/worker/tests/test_live_orchestration.py` (21) and gateway `workerconsumer_test.go` topology tests. Live real-browser runs remain externally-blocked (ToS).
14. DONE (2026-06-02): Live-browser viewer (noVNC) admin login stack. Opt-in `live-browser` Compose profile adds `browser-viewer` (`deploy/small/browser-viewer/Dockerfile` + `entrypoint.sh`: Xvfb + fluxbox + Chromium CDP:9222 + x11vnc + websockify/noVNC:6080), published loopback-only as `UBAG_NOVNC_PORT:7900`; CDP stays internal. Caddy `/novnc/*` route (SAMEORIGIN) plus a dashboard **Take control** viewer (lazy sandboxed iframe, `frame-src 'self'`). Worker `_novnc_url` is now `UBAG_NOVNC_BASE_URL`-configurable but loopback-gated (`_is_loopback_novnc_base`), falling back to `http://127.0.0.1:7900`. Gateway passes `UBAG_REMOTE_BROWSER_ENDPOINT`/`UBAG_BROWSER_HEADED`/`UBAG_BROWSER_ENGINE`/`UBAG_BROWSER_PROTOCOL`/`UBAG_NOVNC_BASE_URL`. Documented in `deploy/small/env.example` + `README.md`; asserted by `tools/check-small-deployment.mjs`. Covered by `apps/worker/tests/test_novnc_base_url.py` (7). **Manual human login only — no credential/cookie/storage-state capture, no CAPTCHA/2FA automation. The Docker image was not built/run here (no Linux Docker engine); only static config + worker/dashboard wiring validated.**

## Documentation Update Rule

Whenever implementation changes, update these files in the same slice:

- `PROGRESS.md` for current status, validation evidence, and resume notes.
- `IMPLEMENTATION_COVERAGE.md` for A-Z coverage state.
- `apps/docs/src/content/docs/implementation-coverage.md` for rendered docs coverage.
- This `AGENT_HANDOFF.md` when the resume procedure, validation evidence, runtime state, or remaining coding queue changes.

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
