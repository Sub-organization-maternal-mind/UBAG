# UBAG agent handoff: 2026-10-08

Read `CLAUDE.md` first, then this file. `PROGRESS.md` is the ledger, but parts of its perf-fleet and voice sections are stale (see section 6).

Working directory this session used: `D:\Projects\UBAG`. `CLAUDE.md` still says `E:\Projects\UBAG`; verify the root before running anything.

## 1. State at handoff

- `main` = `origin/main` = `4f631af` ("chore: remove automated tests, perf-fleet shards and non-lint checks"). Working tree clean.
- Local branches: `main` and `bak-p011` only. Worktrees: `main` plus one opencode worktree in the user's Temp folder, owned by another tool. Leave it alone.
- Remote branches are not cleaned up (see section 3, item 4).
- Production (`ubag.polytronx.com`, SSH alias `vps`, 185.252.233.186):
  - `ubag-vps-gateway-1`, `ubag-vps-browser`, `ubag-vps-chat-reaper` and `ubag-nginx-dashboard` all run `sha-4f631af`. The gateway reports healthy and `UBAG_BUILD_COMMIT=4f631af`.
  - `ubag-voice-demo-gateway` and `ubag-voice-demo-browser` run `ubag/gateway:voice-canary` and `ubag/vps-browser:voice-canary`, up about two days. They are not built from `main`. Owner unknown; do not touch them without asking.
  - Host: 8 vCPU, 24 GB RAM, load average about 15 at last check. Other tenants share the box.
- Public `/v1/*` URLs return nginx 401 from outside, and `/v1/ready` returns 404 at the edge. Verify through the SSH probe, not the public URL.
- CI on `4f631af`: `ci`, CodeQL and dependency graph green (run 37697295356).

## 2. Done

- **Perf and shared-fleet program:** all slice branches are merged. For every `feat/pf-*` and `feat/perf-fleet` branch, `git rev-list --count main..<branch>` is 0. Key PRs: #189 (gitleaks allowlist for synthetic fixtures), #193 (live voice 503 only behind `UBAG_VOICE_LANE_EXCLUSION`), #194 (admission lane locking and bench default), #195 (bench DSN guard; `fleet:*` actions superadmin-only), #196 (DaemonPool LRU flake).
- **DaemonPool LRU flake (PR #196):** `apps/gateway/internal/workerdaemon/pool.go` orders slots by a strictly increasing `Pool.useSeq` counter instead of `time.Now()`. Windows clock ties made eviction pick the wrong warm slot. Verified: the LRU test passed 30 repeated runs, and the `workerdaemon` package tests pass. Not re-verified against the old code.
- **Test strip (`4f631af`):** automated tests, perf-fleet slice shards and non-lint checks removed. Last commit with tests is tag `pre-strip-tests`. Only lint and contract gates remain.
- **Fresh production deploy:** Gateway Image run `37699164269` (workflow_dispatch on `main`) succeeded: build, dashboard and deploy jobs all green. The earlier push run `37697295363` had deployed the same commit.
- **Cleanup:** removed about 70 stale worktrees and deleted local branches merged into `main`. Verified with `git branch -d` (refuses unmerged).
- **Feature flags:** all new behaviour sits behind env flags that are inert by default, except `UBAG_ADMISSION_SHARED`, which is on in production with a kill switch. Flag inventory: `docs/perf-fleet/FLAGS.md`.

## 3. Decisions the owner must make (do not act without an answer)

1. **Rotate `UBAG_VOICE_RELAY_SECRET`.** An earlier probe printed its value into the session transcript. The probe is now fixed; the secret was not rotated. Procedure: generate a new value, update `/opt/docker/ubag/deploy/vps/env.local` on the VPS, then restart `ubag-vps-gateway-1` and `ubag-vps-browser` together, because voice relay pairs them. Verify the voice relay after restart. Do not repeat the old value anywhere. Also check the probe output files in the session scratchpad (`vps-probe-output.txt`, `ctx\prod-probe-2026-10-06.txt`) and remove them if they hold the old value.
2. **`bak-p011` (local):** two unmerged commits, `c74dbad` (run provenance in `tests/load`) and `cb8753a` (workload manifests). They re-add load-test code and tests that the 2026-10-08 strip removed. Keep or delete.
3. **Recovered untracked files:** five files from the dead P1.8 attempt (`feat/pf-p1-8` worktree), not on `main`, rebuilt from the workflow's subagent transcripts into `scratchpad\recovered-untracked\`: `admission_group.go`, `admission_gate_test.go`, `postgres_create_test.go`, `db_pool_bench_test.go`, `postgres_group_test.go`. The rebuild uses recorded Write and Edit calls only; any shell-made edit is missing. Review or discard. Do not merge them without review.
4. **Remote branches:** about 90 merged `feat/pf-*` branches remain on `origin`. Three `origin/ci-logs-*` branches hold commits not on `main` (`ci-logs-gateway`, `ci-logs-gateway-full`, `ci-logs-supplychain`); their provenance is unreviewed. Sixteen Dependabot PRs are open: #197, #87, #86, #85, #75, #74, #47, #45, #41, #40, #39, #30, #29, #28, #27, #26.
5. **Rollback target:** the ledger's rollback is "redeploy `sha-a8880d3`", which is no longer the previous build. Pick a real rollback target and decide whether to run the drill. The drill has never been run.
6. **Helper flags:** `env.local` sets `UBAG_FLEET_MANAGER_URL`, `UBAG_FLEET_MANAGER_TOKEN` and `UBAG_HELPER_NODES` (values not recorded here). The 2026-10-06 ledger says helper flags are unset. Reconcile which is true.

## 4. Remaining work that needs an external resource

No code agent can finish these from the workstation.

| Item | Blocker | Unblocks it |
|---|---|---|
| Phase 1 shared fleet and P4.20 helper canary | OET fleet manager not built. The `oet-dev` host (68.183.32.122) refused TCP/22 on 2026-10-06. | Manager built, helper VPS enrolled, owner console step (see PROGRESS.md). Canary drill: `docs/perf-fleet/CANARY.md`. |
| P5.4 live voice two-way probe | Needs a person on a real call. `ready_controls` is empty. | Run `docs/perf-fleet/voice-activation-probe.md`. |
| P7.5 to P7.7 relay A/B gate | Needs a Linux lab host, real libopus and an isolated container. Verdict is UNEVALUATED. The exact command was in `docs/perf-fleet/slices/P7.5.md`, which is gone from `main`. | Lab host. Recover the shard with `git show 4f631af^:docs/perf-fleet/slices/P7.5.md`. |
| P7.8 Rust relay | Not started. | P7.5 to P7.7 verdict. |
| P7.9 capacity report and P1.8 lab sweep | No isolated lab host, no Docker here. Benches need a loopback Postgres DSN; `benchutil.CheckDSN` refuses other hosts. The report is labelled non-authoritative and quotes no number. | Lab host. Run the 1/2/5/10/20 ladder with a 60-minute steady state. |
| P0.4 legacy-tool CI validation | Never run. | Owner decision. |
| P6.6 warm-resume fast path | Built, merged, off by default (`UBAG_WARM_RESUME_FASTPATH`). Not live-verified. | Live verification after a flag decision. |
| Rollback drill | Never exercised. | Decision in section 3, item 5. |
| Provider logins | Human only, by product rule. | `tools/provider-refresh/`, run by a person at the browser. |
| Compose gaps | Flags whose Compose column in `FLAGS.md` is `none` have no effect when set only in `env.local`. | A reviewed compose line per flag (`ROLLOUT.md` section 1). |

## 5. Rules for the next agent

- **Safe mode is a hard constraint.** User-owned sessions only. No automated login, credential scraping or storage, or CAPTCHA solving.
- **Never print or commit secret values.** For production, use `tools/vps-ubag-probe.sh` (allowlisted values; everything else as name and length). Do not write ad-hoc env dumps.
- **No automated tests.** They were removed on 2026-10-08. Routine checks are targeted: `ruff check apps/worker adapters`, a single `go vet` or `tsc`, or `pnpm lint` only when a contract changed. The user runs full verification and reports back.
- **Push to `main` deploys to production.** `.github/workflows/gateway-image.yml` triggers on `main` for listed paths and deploys over SSH. Migrations apply on the shared VPS. Get an explicit yes first.
- **Deploy:** `gh workflow run gateway-image.yml --ref main`. Watch with `gh run list --workflow gateway-image.yml --limit 3`. The deploy job health-checks and rolls back on failure.
- **Commits:** DCO `Signed-off-by` on every commit, as the repo requires.
- **Shared box:** do not restart or change containers you do not own, including the voice-demo containers.

## 6. Stale docs to reconcile

- `PROGRESS.md` cites `docs/perf-fleet/slices/` at lines 5, 304, 413 and 434. That directory was removed in `4f631af`.
- `PROGRESS.md` (2026-10-06 section) says production cannot run live voice because `UBAG_VOICE_RELAY_SECRET` is unset and `UBAG_VOICE_STORE` is memory. The 2026-10-08 probe shows the secret set and `UBAG_VOICE_STORE=postgres`. Reconcile.
- `CLAUDE.md` gotcha names `E:\Projects\UBAG` as the root. This session ran from `D:\Projects\UBAG`.
- `AGENT_HANDOFF.md` and `PROGRESS.md` do not yet mention this handoff.

## 7. Commands

```bash
ssh vps 'bash -s' < tools/vps-ubag-probe.sh
```

```bash
gh workflow run gateway-image.yml --ref main
```

```bash
gh run list --workflow gateway-image.yml --limit 3
```

```bash
ruff check apps/worker adapters
```
