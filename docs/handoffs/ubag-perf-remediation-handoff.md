# UBAG Performance Remediation — Agent Handoff (2026-07-15)

## MISSION
Operator reported: "the UBAG project is very very slow — the backend and the APIs are very slow. Fix this A-Z 100%."
Then: activate the "built but never plugged in" features.

## HARD INVARIANT (operator directive — overrides speed)
**Speed up freely, but NEVER compromise results.** The browser automation must capture the
ENTIRE AI-provider response and return it through UBAG correctly and completely.
Optimize *around* the capture, never shorten the capture.
UBAG powers **RadioPad, a RADIOLOGY REPORTING product** → a fast wrong / incomplete /
cross-patient report is far worse than a slow one.

## VERIFIED PROD TOPOLOGY (checked live — do NOT trust static analysis here)
- Host `185.252.233.186`, root SSH works passwordless. 6 CPU / 12 GB, **busy shared multi-tenant box**
  (~15 app stacks: RadioPad, laravel, insight, docduty, pm, oet, asterisk…). Load ~34-52 = bursty
  run-queue spikes, NOT CPU-saturation/IO/swap. UBAG gateway itself uses ~3% CPU.
- `UBAG_GATEWAY_STORE=postgres` (pool 20) — **NOT sqlite** (static analysis wrongly assumed sqlite).
- `UBAG_EXECUTOR_MODE=file` (file-spool) — **NOT nats** (NATS container runs but is unused).
- Worker is **bundled inside the gateway image** (`/app/apps/worker/run_live_worker.py`), spawned
  **per job**, connects via CDP to the shared Chromium in `ubag-small-browser-viewer-1`
  (`UBAG_REMOTE_BROWSER_ENDPOINT=http://172.31.0.5:9223`). ONE shared browser + ONE profile.
- RadioPad → UBAG via `RADIOPAD_UBAG_BASE_URL=http://ubag-small-gateway-1:8080` (docker network,
  static app-secret). **Host port binding is irrelevant** → container recreates don't disrupt RadioPad.
- RadioPad failover: `gemini_web,deepseek_web`; `RADIOPAD_UBAG_TIMEOUT_MS=270000`.
- Real job durations: avg 53s, max 161s. Worker max runtime 420s. No job sets `options.timeout_seconds`.

## DEPLOY MECHANICS — ⚠️ REPLACED 2026-07-15: **CI builds, prod only pulls. NEVER build on the VPS.**
Operator directive: no builds/linting/compute on the VPS. It is a 6-core box shared with ~15
co-tenant stacks; a gateway Go build there took ~25-30 min and competed with live traffic. The
same build on a GHA runner takes **~3 min**.

**Deploy = push to `perf/latency-az`. That is the whole procedure.**
`.github/workflows/gateway-image.yml` builds the image, pushes
`ghcr.io/sub-organization-maternal-mind/ubag-gateway:sha-<commit>`, then deploys it automatically.
Watch it with `gh run watch <id>`; `gh run rerun <id> --failed` retries just the deploy.

How the deploy job reaches prod without any long-lived secret:
- It authenticates to GHCR with the **run's own ephemeral `GITHUB_TOKEN`** (expires with the job)
  and pipes it to the box over stdin for a single pull. Verified: `/root/.docker/config.json` is
  `{"auths":{}}` after a deploy — nothing persists, nothing to rotate.
- The image stays **private**: the org **forbids public packages** by admin policy (Public/Internal
  are greyed out in package settings). Do NOT work around that by republishing the image as a public
  Release asset — that circumvents a deliberate org control.
- The CI key (GHA secret `UBAG_DEPLOY_SSH_KEY`, host in `UBAG_DEPLOY_HOST`) is pinned by
  `command=` in root's `authorized_keys` to `deploy/small/ci-deploy.sh`. Verified it cannot open a
  shell, cannot run `id`, and rejects non-`sha-<40hex>` tags — a leaked key cannot roam the box.
- `ci-deploy.sh` pins `UBAG_GATEWAY_IMAGE` in the VPS-only `env.local`, recreates the gateway,
  health-checks through nginx→gateway (RadioPad's real path), and **rolls back to the previous image**
  if it does not come up.

**GOTCHA (cost 3 failed deploys):** GHCR layer pulls died mid-transfer with `connection reset by
peer` to `2606:50c0:8003::154`. GitHub CDN (`pkg-containers.githubusercontent.com`) is the only
GitHub host with real IPv6 (`ghcr.io`/`github.com` are IPv4-only) and it **accepts v6 connections
but resets large transfers = IPv6 PMTU blackhole**; because it fails mid-transfer, Happy-Eyeballs
fallback never triggers. `/etc/gai.conf` **does not fix this** — docker/containerd are Go and Go's
resolver ignores gai.conf (it only steers glibc apps like curl, which is a misleading green test).
Fixed at the kernel level, scoped to GitHub CDN so co-tenant IPv6 is untouched:
`ip -6 route replace unreachable 2606:50c0::/32`, persisted as
`ubag-github-v6-blackhole.service` (enabled). Verified google/cloudflare v6 still 301.

- `/opt/ubag` on the VPS is still a **git checkout** on branch **`perf/latency-az`**, but it is now a
  *deploy* checkout (compose file + gitignored `env.local` + `ci-deploy.sh`), **not** a build source.
  Consider dropping the `build:` block from the compose gateway service so nobody can accidentally
  trigger a 30-min compile on prod.
- Prod base commit was `4b1f88cc2` (main), which had an **uncommitted local drift** = the
  sign-in-required (`provider_login_required`) work. That drift is now committed as `c41a9d4`.
- Local repo `E:\RadioPad MEGA Folder\UBAG` is on the same branch `perf/latency-az`.
- **Sync local → VPS via thin git bundle** (full bundle is 28MB and times out; thin is KB):
  ```
  git bundle create /path/phase.bundle <lastVPScommit>..HEAD
  scp /path/phase.bundle root@185.252.233.186:/tmp/x.bundle
  ssh root@185.252.233.186 'cd /opt/ubag && git fetch /tmp/x.bundle HEAD:refs/remotes/bundle/x && git merge --ff-only refs/remotes/bundle/x'
  ```
  NOTE: the bundle exposes only the ref **`HEAD`** (not the branch name).
- Build + deploy:
  ```
  cd /opt/ubag && docker compose -f docker-compose.small.yml --env-file deploy/small/env.local build gateway
  docker compose -f docker-compose.small.yml --env-file deploy/small/env.local up -d --no-deps --force-recreate gateway
  ```
- **Build times:** Go change → ~25-30 min (full recompile, modernc sqlite is huge). Python-worker-only
  change → ~1 min (Go layer cached).
- `deploy/small/env.local` is **gitignored** → env changes live only on the VPS.

## SHIPPED + VERIFIED LIVE IN PROD
| Commit | Change | Verification |
|---|---|---|
| `c41a9d4` | Phase 0 infra: gateway `cpus 1→3`, `GOMAXPROCS=3`, `GOMEMLIMIT=1800MiB`, `mem 2g`, spool `500→150ms` | GOMAXPROCS=3 confirmed in container; Go 1.25 had auto-pinned it to **1** under the 1-CPU cgroup |
| `f08c5e2` | **A1** skip synchronous Merkle audit write on ALLOWED reads + **A2** bounded job-signal event scan via new optional `jobs.RecentEventLister` | **8 live polls → audit rows 14402→14402 (0 added)**. Previously **99.5% of 26k audit rows** were `job:read`/`browser:read` allows. Poll still returns correct status. |
| `426aaea` | Worker: `_first_visible` races selector candidates vs ONE deadline (was full timeout **per candidate** → a drifted `response_container` could stall a job for MINUTES); completion streaming-indicator probe 500→150ms | Code confirmed in running image. Completeness-safe by construction. |
| `b96bdf2` + `6df4d85` | Opt-in **stale-job reaper**, adapted to prod's tuple-based `ConcurrencyRegistry.Release(tenant,target,identity)` (the CAS branch's `ReleaseForJob` does NOT exist in prod) | 4/4 reaper tests pass; live: 0 jobs wrongly timed-out, no errors |

**Live env (VPS `deploy/small/env.local`, gitignored):** `UBAG_WORKER_POLL_INTERVAL_MS=150`,
`UBAG_JOB_REAPER_ENABLED=true`, `UBAG_JOB_MAX_LIFETIME_SECONDS=1800` (30 min idle ≈ 4× worker max —
reaper only catches genuinely-dead jobs; a progressing job keeps `UpdatedAt` fresh and is never reaped).

## ✅ DEPLOYED + VERIFIED LIVE 2026-07-15 (was "committed locally, not deployed")
Live image: `ghcr.io/sub-organization-maternal-mind/ubag-gateway:sha-3dabe70…` (contains `8c78fb9`).
Verified after deploy: `/v1/ready` → `ready:true` (all 7 checks) through nginx→gateway; `GOMAXPROCS=3`
preserved; `UBAG_EXECUTOR_MODE=file`; **no ABAC env → enforcer nil → exact no-op**; 0 startup errors;
audit read-allow rows **14402 → 14402** (f08c5e2 audit-skip still holding); no unexpected `timed_out`.

**Pre-deploy check worth keeping:** prod's `env.local` sets `UBAG_NATS_WORKER_ACK_WAIT_MS=35000`,
which is ≤ `UBAG_WORKER_MAX_RUNTIME_MS=420000` — exactly what the new invariant refuses to start on.
It does **not** brick prod because the check lives inside `case "nats":` (serve.go:1075) and prod is
`case "file":`, which returns at serve.go:1059 before ack-wait is ever parsed. Two consequences:
the compose default change is **inert on prod** (env.local overrides it), and if anyone ever flips
prod to NATS the gateway will refuse to boot until that env is raised — the invariant working as
designed, not a bug.

- **`8c78fb9`** — "activate ABAC as a safe no-op scaffold; make NATS ack-wait a hard invariant"
  - ABAC: fixed the real bug (`NewEnforcer` compiled every rule then threw programs away via `_ = prog`,
    so `Allow` re-Compiled CEL **per rule per request**). Now compiled once; `Allow` only Evals.
    Enforcer constructed in `serve.go` **only** when `UBAG_ABAC_BUNDLE` is set → nil otherwise = exact
    no-op (cannot 403 RadioPad). Bad bundle fails startup loudly.
  - NATS: compose hardcoded `UBAG_NATS_WORKER_ACK_WAIT_MS=35000`, overriding the adaptive
    (maxRuntime+5s) default → with 420s runtime JetStream would redeliver a **still-running** job →
    duplicate execution on the shared browser. Compose now empty (adaptive), and **startup refuses any
    ack-wait <= max runtime** (code invariant, not operator discipline). Prod keeps file-spool.
  - ~~TODO: bundle → VPS → build → recreate~~ **DONE 2026-07-15 via the CI pipeline above.**
    The thin-git-bundle dance is obsolete for gateway changes: just push the branch.

## REMAINING WORK (operator chose: "Warm-browser + safe scaffolds", NO fuzzy semantic caching)

### 1. Warm-browser reuse (THE only real speed win) — XL, NOT started
Build **flag-off** behind `UBAG_WORKER_DAEMON=1` (default off). Concurrency pinned to **1 job at a
time per browser** (do NOT enable multi-tab concurrency: sync-Playwright objects are thread-bound, and
two pages driving the same provider account risks CAPTCHA/lockout + cross-patient bleed).
- **LAYER A** `page_driver.py`: make `open()` idempotent (today `engine.py:140` re-attaches CDP + opens a
  NEW page every job); add `prepare_for_next_job(selectors)` = health probe → force fresh chat → **prove
  empty** → rebuild tab on any doubt.
- **LAYER B** new long-lived daemon: reads job envelopes on stdin, holds warm drivers keyed by
  `(tenant, provider, identity)`, injects into `engine.iter_events(payload, driver=warm)` so
  `owns_driver=False` and the page survives (`engine.py:106,360`). One job in flight at a time.
- **LAYER C** Go `DaemonWorkerRunner` implementing the existing `WorkerRunner` interface
  (`workerconsumer.go:103`), one persistent `exec.Cmd` with piped stdin/stdout, replacing per-job spawn
  (`workerconsumer.go:810`); wire in `serve.go` behind the flag. Per-job deadline must move INTO the
  protocol (the Go-side MaxRuntime ctx no longer bounds a subprocess).

**THE SAFETY GATE (key design — solved):** `ProviderSelectors` has **no** turn/emptiness selector, and
**guessing one is the worst outcome** (it silently matches nothing → reads "empty" → passes → a prior
patient's report can bleed in). **Solution: reuse the already-verified, drift-baselined
`response_container` as the emptiness probe.**
- After forcing new-chat on a reused tab: probe `response_container`.
- **Present** → prior turn exists → cannot prove empty → **rebuild tab cold** (today's safe behavior).
- **Absent** → proven empty → submit.
- **If it ever drifts** → probe reads "absent" → submit → the later read raises `DriftDetectedError`
  → job **fails loudly instead of returning a prior patient's answer**. Fail-safe in every branch.
- **NEVER touch** `stream_response`'s settle-window loop (`page_driver.py:~909-928`) or
  `read_final_response` — those ARE the full-capture guarantee. The settle window is **essential for
  Gemini, which exposes NO reliable streaming indicator**; do not shorten it.
- Reality check: if a provider renders an empty `response_container` on a fresh chat, the gate correctly
  refuses reuse → safe, but no speedup. Needs a real job to confirm (drives the operator's live session).

### 2. Answer cache (exact-match) — default OFF, safe scaffold, ~ZERO benefit
`responsecache.Cache.Lookup/Store` have **zero callers**. Wire: Lookup before dispatch (job-create path),
Store in `workerconsumer.go` success branch on **`StatusCompleted` ONLY** (never
`CompletedWithWarnings`/partial). Cache key = `BuildKey(tenant, app, target, command, **Input**)` →
**the `Input` bytes MUST fold the COMPLETE unique payload** (full prompt/findings + options + template +
conversation, canonical JSON) or two different reports could collide. `PrivacyMode` bypass already exists.
Stays gated by `UBAG_CACHE_ENABLED` (default off). Plumbing already reserved:
`JobOptions.CachePolicy`, `JobResultEnvelope.Cached/CacheSource`, `buildJobResultEnvelope` reads
`cached`/`cache_source`. **Verified ~zero benefit: radiology prompts are unique PHI → ~0 cache hits**,
while adding PHI-in-cache + staleness.

### 3. Dragonfly-backed stores — default OFF, safe scaffold, ~ZERO benefit
Dragonfly container RUNS but **no Go code connects** (no redis client in go.mod). Implement
`responsecache.Store` + `ratelimit.Store` backed by it, wired via `UBAG_DRAGONFLY_URL` (unset = unused).
Short dial/read timeouts + graceful degradation (a hung Dragonfly must never stall/fail a job).
**Verified ~zero benefit: prod runs a SINGLE gateway replica**, so "correct across replicas" is moot.

## VERIFIED ANALYSIS — the "dormant features" picture (12-agent adversarial review)
- 🛑 **Semantic/"smart" cache — DO NOT ACTIVATE.** Different patients' radiology prompts are **95-99%
  textually identical** (same modality/boilerplate), differing only in MRN / a measurement / laterality.
  A ≥0.97 cosine match returns a **NON-identical stored body** → **the wrong patient's report**. Verdict
  SOUND / not-recommended. The vector tier is a stub; leave it.
- ⚠️ Answer cache / Dragonfly / NATS — safe-ish but ~zero benefit here (see above). Keep file-spool.
- ✅ ABAC — safe (never touches report content); only risk is availability if rules are mis-scoped.
- ✅ Warm-browser — the one real win; high stakes.
- **Intentionally OFF by design (edge profile, NOT bugs):** rate limiting, webhooks, SSO, SCIM, SIEM,
  MFA/JIT, multi-tenant RBAC, region/geo.

## GOTCHAS / TRAPS (learned the hard way)
1. **Pre-existing flaky test:** `TestProcessWorkerRunnerRunsPythonWorkerFromGatewayEnvelope` spawns
   Python; on Windows `exec.LookPath("python")` finds the **Windows-Store stub** (so it doesn't skip) and
   fails ~50%. **Environmental, not a real break** — passes on Linux/CI. It can litter a stray
   `executor/Python/` dir; delete it.
2. **`pgrep -f "compose.*build gateway"` false-positives** on your own wait-loop command lines → reports
   "STILL BUILDING" when the build finished. Check the **image `.Created` timestamp** / `"Image ... Built"`
   in the log instead.
3. **Background SSH wrappers exit 255** ("failed") when their long-lived connection drops — this does
   **NOT** mean the build failed. Verify via the image timestamp + build log.
4. **Host `127.0.0.1:8080` binding disappears after a gateway recreate** — irrelevant (RadioPad uses the
   docker network). Benchmark via the container name or the bridge IP instead.
5. `/v1/ready` is a **bad benchmark target** (it runs many Postgres readiness queries, ~55-90ms).
6. Prod `main` is BEHIND GitHub `origin/main` (472f2a7) and has local drift — **do not blindly pull main**;
   the deploy branch `perf/latency-az` is the source of truth.
7. Nothing has been pushed to GitHub origin — work exists on local + VPS only.

## VERIFICATION RECIPES
```bash
# health via RadioPad's real path
docker exec ubag-small-nginx-dashboard-1 wget -qO- -T5 http://gateway:8080/v1/ready
# audit-skip proof (poll a job N times; count must NOT move)
docker exec ubag-small-postgres-1 psql -U ubag -d ubag -tAc \
  "select count(*) from gateway_audit_log where action='authorize:job:read' and outcome='allow';"
# jobs by status (reaper must never create unexpected timed_out)
docker exec ubag-small-postgres-1 psql -U ubag -d ubag -tAc \
  "select status,count(*) from gateway_jobs group by status order by 2 desc;"
# real request latencies from logs
docker logs --since 20m ubag-small-gateway-1 2>&1 | grep '"http request"'
```
**Completeness gate for ANY worker/automation change:** run a real report per provider and **diff the
returned response text vs a known-good baseline**. No truncation, ever.

## RECOMMENDED NEXT STEPS
1. Deploy `8c78fb9` (ABAC + NATS) — bundle → build (~25 min) → recreate → verify healthy.
2. Build warm-browser (flag-off) per the design above; test; deploy; have the operator run a real report
   to see if reuse engages + confirm response completeness.
3. Optionally wire the answer-cache + Dragonfly scaffolds (default off, ~zero benefit).
4. **Infra (operator decision, affects other tenants):** the shared box is over-tenanted — consider CPU
   reservations/`cpu_shares` for gateway + browser-viewer, or fewer co-tenants / more cores.
