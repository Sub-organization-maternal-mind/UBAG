# Handoff: Enable Live Browser-Automation Worker (Production)

**Purpose:** Turn on UBAG's live automation path so submitted jobs actually drive the
operator's already-logged-in Chromium (via CDP) and return normalized results.
Today the gateway accepts jobs but runs them through the **mock** worker only.

Paste this whole file into a fresh chat to execute the build with full context.

---

## 1. Production access & deploy flow

- **VPS:** `ssh oet-dev` (root@68.183.32.122, key `~/.ssh/id_ed25519`). Project at `/opt/ubag`.
- **Repo:** `https://github.com/Sub-organization-maternal-mind/UBAG.git`, branch `master`.
  The VPS has **no GitHub auth** — deploy by git bundle over SSH:
  ```bash
  # from local repo after committing+pushing:
  git bundle create /tmp/ubag.bundle master
  scp -o StrictHostKeyChecking=no /tmp/ubag.bundle oet-dev:/tmp/
  ssh oet-dev "cd /opt/ubag && git stash 2>/dev/null; git fetch /tmp/ubag.bundle master && git merge FETCH_HEAD --ff-only"
  ```
- **Compose:** `docker compose --env-file deploy/small/env.local -f docker-compose.small.yml ...`
- **Dashboard:** https://ubag.polytronx.com/dashboard/ — Basic Auth `operator` / `<see /opt/ubag/deploy/small/.htpasswd on the VPS>`
- **noVNC viewer:** https://ubag.polytronx.com/novnc/vnc.html (same Basic Auth) → VNC password: `UBAG_BROWSER_VNC_PASSWORD` in `/opt/ubag/deploy/small/env.local`
- **Gateway app secret:** `UBAG_APP_SECRET` in `/opt/ubag/deploy/small/env.local` on the VPS — NEVER commit the value
  (nginx injects this into `/v1/*` server-side; for direct API tests use Basic Auth + `Ubag-Api-Version: 2026-05-22`.)
  <!-- SECURITY (2026-07-11): this doc previously committed the live UBAG_APP_SECRET,
       VNC password, and dashboard Basic Auth password in plaintext. They were
       redacted and MUST be treated as exposed — rotate all three on the VPS. -->

## 2. Current production state (verified)

- Running: `gateway`, `nginx-dashboard`, `postgres`, `minio`, `dragonfly`, **`browser-viewer`** (all healthy).
- `browser-viewer` = real persistent Chromium on Xvfb, streamed over noVNC. **CDP live on `browser-viewer:9222`** (Chrome 148 confirmed via `/json/version`). Internal-only (never published).
- Dashboard fully working (17 pages, real data). Edge Basic Auth + server-side Bearer injection.
- **Executor is OFF:** `UBAG_EXECUTOR_MODE=noop`, `UBAG_WORKER_CONSUMER_ENABLED=false`. Jobs are accepted but never executed.
- `UBAG_REMOTE_BROWSER_ENDPOINT=http://browser-viewer:9222` already set.

## 3. The worker invocation contract (IMPORTANT — already reverse-engineered)

`apps/gateway/internal/executor/workerconsumer.go:629` (`ProcessWorkerRunner.RunWorker`):
```go
command := exec.CommandContext(runCtx, python, script, "--input", "-")
command.Stdin = bytes.NewReader(payload)   // the job payload JSON
command.Stdout = stdout                     // worker writes JSONL here
// each stdout line is json.Unmarshal'd into jobstore.WorkerEvent (JSONL stream)
```
- `python` = `UBAG_WORKER_PYTHON` (default `python`), `script` = `UBAG_WORKER_SCRIPT`
  (default `apps/worker/run_mock_worker.py`).
- So any worker entrypoint must: **read the job payload JSON from stdin (`--input -`)** and
  **emit `jobstore.WorkerEvent` objects as JSONL on stdout**, then exit 0.
- The mock does this via `ubag_worker.runner.emit_jsonl(payload, sys.stdout)`.

## 4. What already exists (the live code is written)

In `apps/worker/ubag_worker/`:
- `live/page_driver.py` → `PageDriver` (ABC), `MockPageDriver` (offline), **`PlaywrightPageDriver`** (real; connects over CDP).
- `live/engine.py` / `live/engines.py` → **`LiveSessionEngine`** (orchestrates a run; emits the manual-action / token / completed events).
- `live/events.py` → live event types (check how these map to `jobstore.WorkerEvent` JSONL).
- `live/selectors.py` → per-provider `ProviderSelectors` + `PROVIDER_SELECTORS`.
- `adapter_registry.py` + `adapters/<provider>/` (chatgpt_web, claude_web, deepseek_web, gemini_web, mistral_lechat, perplexity_web, generic_chat, generic_form, mock) — each adapter `run()` fails closed (safe-mode); `run_live()` delegates to `LiveSessionEngine`.
- `live/ONBOARDING.md` documents the live adapter design + invariants (ToS-safe: never logs in, never ingests cookies/credentials, never solves CAPTCHAs).
- `run_mock_worker.py` = thin wrapper → `ubag_worker.cli.main` (mock). `cli.py` is **mock only**.

## 5. What's missing — the build

### 5a. Live worker entrypoint  `apps/worker/run_live_worker.py`
Mirror `run_mock_worker.py`'s **stdin→JSONL-stdout** contract, but run the live path:
1. Parse `--input -` (read payload JSON from stdin), same args as the mock CLI (`--payload`, `--input/-i`, `--output/-o`).
2. Resolve the adapter for `payload["job"]["target"]` via `adapter_registry`.
3. Build a `PlaywrightPageDriver` that `connect_over_cdp(UBAG_REMOTE_BROWSER_ENDPOINT)` (`http://browser-viewer:9222`). Confirm the exact constructor/params in `live/page_driver.py:217` (`PlaywrightPageDriver`).
4. Run `adapter.run_live(payload, driver=...)` (or `LiveSessionEngine(selectors).run(payload, driver=...)` — confirm the adapter `run_live` signature).
5. Map the engine's emitted events to `jobstore.WorkerEvent` JSONL on stdout (reuse `live/events.py` + `runner.emit_jsonl` style). **Verify the event schema matches** what `workerconsumer.go` unmarshals (queued/running/token/completed/failed, plus `session.manual_action_required`).
6. Install graceful-drain shutdown like `run_mock_worker.py` but with a REAL orchestrator (it has in-flight jobs).

### 5b. Install live deps in the worker runtime
The gateway image **lacks `playwright`/`patchright`** (`pip install '.[live]'` from `apps/worker/pyproject.toml` optional-deps `playwright>=1.49`, `patchright>=1.49`).
- Connecting to a **remote** CDP browser needs only the Python lib, **not** `playwright install` browsers (browser-viewer provides Chromium).
- Edit `deploy/small/gateway.Dockerfile` to `pip install playwright patchright` (and ensure `apps/worker` + `adapters/` are importable — they already ship in the image at `/app/apps/worker`, `/app/adapters`).
- **Decision:** install into the gateway image (embedded consumer) — simplest — OR add a dedicated `worker` service. Embedded is fine for the small profile.

### 5c. Wire dispatch + consumer  (`deploy/small/env.local`)
```
UBAG_EXECUTOR_MODE=file               # embedded file-spool consumer (no NATS needed). Or 'nats' + queue profile.
UBAG_EXECUTOR_SPOOL_DIR=/var/lib/ubag/executor-spool
UBAG_WORKER_CONSUMER_ENABLED=true
UBAG_WORKER_PYTHON=/usr/bin/python3   # confirm python path in image
UBAG_WORKER_SCRIPT=/app/apps/worker/run_live_worker.py
UBAG_REMOTE_BROWSER_ENDPOINT=http://browser-viewer:9222   # already set
UBAG_WORKER_MAX_RUNTIME_MS=120000     # raise from 30s — live runs are slower
```
(If `nats`: also `docker compose ... --profile queue up -d nats` and set `UBAG_NATS_*`.)

### 5d. Rebuild + redeploy
```bash
ssh oet-dev "cd /opt/ubag && docker compose --env-file deploy/small/env.local -f docker-compose.small.yml build gateway && \
  docker compose --env-file deploy/small/env.local -f docker-compose.small.yml --profile live-browser up -d"
```

## 6. Test plan

1. **Pipeline test (no login needed):** submit a `mock` target job; confirm it reaches `completed` with a normalized result (proves dispatch→consumer→worker→result works end-to-end).
   ```bash
   curl -s -u operator:GtIEv4fBYe5DApUHxGGrjK8A -H 'Ubag-Api-Version:2026-05-22' \
     -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
     -d '{"job":{"target":"mock","command_type":"send_message","input":{"prompt":"ping"}},"client":{"app_id":"t","app_version":"1","sdk":{"name":"curl","version":"0"}}}' \
     https://ubag.polytronx.com/v1/jobs
   # poll GET /v1/jobs/{id} until terminal
   ```
2. **Live test (after manual login):** operator logs into e.g. ChatGPT via noVNC, then submit a `chatgpt_web` job. Expect either a real normalized answer, or `session.manual_action_required` if not authenticated. Watch it on the dashboard **Browser Sessions** page (xterm log + noVNC).

## 7. Risks / gotchas
- **Event-schema mismatch:** the live engine's events must serialize to the exact `jobstore.WorkerEvent` JSON the Go consumer expects. This is the most likely sharp edge — diff `live/events.py` output vs `ubag_worker/runner.py emit_jsonl` output and align.
- **`PlaywrightPageDriver` CDP params:** confirm it takes the CDP ws/http endpoint and a context/profile; browser-viewer exposes CDP at `:9222`.
- **Max runtime:** live provider round-trips exceed the 30s default → bump `UBAG_WORKER_MAX_RUNTIME_MS`.
- **ToS invariants (do not break):** never auto-login, never ingest credentials/cookies, never solve CAPTCHAs. Unauthenticated → emit `session.manual_action_required` + loopback noVNC URL and wait for the human.
- **Concurrency:** one shared Chromium profile — serialize live jobs or use separate contexts; check `live/orchestrator.py`.

## 8. Definition of done
- `make`/curl mock job → `completed` with normalized result via the live worker path.
- After operator login, a `chatgpt_web` (or other) job returns a real normalized answer (or correct `manual_action_required`).
- Dashboard Browser Sessions shows the live instance/contexts/tabs and the xterm log.
- Committed + pushed; VPS redeployed from that commit.
