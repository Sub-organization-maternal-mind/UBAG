# Security review fixes (perf-fleet)

Branch `feat/pf-review-security`, base `feat/perf-fleet`. All four findings were re-verified against the code; all four were real. Three are fixed in full, one (finding 2) is fixed at the minimum level the review allowed.

## Fixed

1. **P4.9, fleet node id in tenant-visible job events (medium).** Confirmed: `helperingest.go` stamped `data.helper{node_id, lease_generation}` on every helper event and on the gateway failure event, and job events are returned verbatim over REST, SSE, gRPC and webhooks. The stamp is removed and any helper-supplied `data.helper` key is dropped; `data.attempt_id` stays. Provenance now lives in the attempt ledger and the audit chain. Tests assert no event data contains the node id (ingest, reconcile gate, remote runner). `CANARY.md`, `RUNBOOK.md` and `P4.9.md` no longer tell operators to read `data.helper.node_id`.
2. **P1.6, isolation flags dropped by the worker env allowlist (medium).** Confirmed: `UBAG_PROFILE_OPTIONS_POLICY` and `UBAG_WARM_RESUME_FASTPATH` were neither in `workerdaemon.AllowedEnv` nor in `docker-compose.vps.yml`. Both are now allowlisted and passed through compose with an empty default (FLAGS.md `Compose` column updated; `flag-graduation-check` passes). Test: `TestAllowedEnvForwardsIsolationFlags`. Defaults are unchanged (legacy / off).
3. **P4.8, helper dispatch not audited (medium).** Confirmed: no non-test caller of the attempt audit events. `RemoteWorkerRunner` now appends `attempt.granted` through `audit.Fleet.Guard` on the job's tenant chain before `BeginAttempt` (an unwritable chain holds the job back with reason `audit_unavailable`; nothing is leased or sent), and `attempt.committed` when the attempt ends cleanly. Records carry ids and the generation only. The remote-run test asserts both. CANARY.md step 4 now checks the chain.
4. **P5.5, voice lane exclusion pinned by sessions that can never connect (medium).** Confirmed: with no relay secret the media hub refuses every session, yet `POST /v1/voice/sessions` still reserved a live lease that the lane probe counted for every other tenant's jobs. Live session create now returns 503 `UBAG-VOICE-MEDIA-UNAVAILABLE-007` when the media plane reports it can never connect (`MediaHub.MediaAvailable`: dialer without a relay secret; `NodeRouter.MediaAvailable`: a node host exists or the local hub is available). Utterance mode and planes that do not implement the check are unaffected. Tests: `TestVoiceLiveSessionRefusedWhileMediaCannotConnect`, `TestMediaHubAvailabilityFollowsTheRelaySecret`.

## Not done (scoped down, with reason)

- Finding 2, gateway-side rejection of tenant-supplied `user_data_dir` / `profile_dir` / `profile_path` at job create: a behaviour change on the live job-create path (legacy callers may send these), so it is not folded into a review fix. Follow-up: decide the contract change, then strip or reject server-side. The "log the effective policy at worker start" and the all-`UBAG_*`-flags-vs-AllowedEnv test were also not added.
- Finding 3, `asset.token_issued`, `viewer.opened` and `Fleet.NodeEvent`: no helper asset-token or viewer path exists yet (D3: no viewer broker), so there is nothing to hook; documented in CANARY.md.
- Finding 4, the unknown-lane branch of `LaneProbe` (an instance with no endpoint holds every lane), a connected-only or time-bounded hold, and a default-off switch: left as is. The branch is deliberate fail-closed behaviour, and with the 503 above a session that cannot connect no longer exists to trigger it. A session that can connect (relay secret set) legitimately holds its browser.

## Checks run

`go vet` on executor, voice, httpapi, helperclient, workerdaemon; `go test` for `internal/executor`, `internal/helperclient`, `internal/serve`, `internal/workerdaemon`, `internal/voice` (targeted) and `internal/httpapi -run Voice`; `node tools/flag-graduation-check.mjs`.

## Not run

Full gateway suite, `pnpm check`, Postgres-gated tests.

## Round 2: P1.8 benches, fleet read view, committed production posture

Branch `feat/pf-review-security-p18` (the earlier branch name was taken by another worktree), base `feat/perf-fleet`. All three findings were re-verified against the code and were real; all three are fixed.

### Fixed

1. **P1.8 benches could write to a shared or production Postgres (medium).** `benchutil.OpenPostgres` accepted any non-empty `UBAG_TEST_POSTGRES_DSN`. It now calls `benchutil.CheckDSN`, which refuses unless every host (fallbacks included) is loopback, private, link-local or a unix socket, or is listed in the new bench-only `UBAG_BENCH_ALLOW_HOST` (wildcards ignored), and the database name contains `bench` or `test`. This mirrors `checkTarget` in `tests/load/acceptance.mjs`. The admission bench no longer uses the real `global:all` lane key (`bench:global` / `bench:tenant`, extra lanes opt-in as before). The P1.8 runbook states both rules. Test: `TestCheckDSN`. The audit bench still appends to the append-only chain, but only to a database that passed the guard.
2. **`fleet:read` / `fleet:manage` on tenant-level roles (medium).** Both views are cross-tenant (node ids, capacity, held-job counts over every tenant), while operator and admin are per-(tenant, app) roles. The actions are removed from operator and admin in `authz.go` and `packages/security/src/rbac.ts`; only the platform-level superadmin holds them (fast path). The action names stay in the contract (`UBAG_ACTIONS`), so `fleet:manage` is not lost for the route that will enforce it. Tests, OpenAPI text and docs were updated (`TestFleetActionsSuperadminOnly`; the httpapi fleet tests now run as superadmin and assert operator/admin get 403). The dashboard already treats 403 as "fleet panel not available". Consequence: a single-tenant deployment that wants the fleet panel must use a superadmin credential. Earlier slice notes (P2.5, P6.1, P6.4) still say "operator and admin"; this entry supersedes them.
3. **Production posture in public docs (medium).** `ctx/BINDING.md` section 1, `slices/P0.1.md`, `slices/P0.2.md`, `slices/P0.13.md`, `slices/P0.14.md` and `ROLLOUT.md` no longer state the deployed commit, container and host sizing, co-tenancy or the canary pair, nor that the relay secret is empty in production. They keep env var names and documented defaults only ("unset by default"). The production-specific record belongs in a private ops note held by the owner. Git history still contains the earlier text; rewriting history is the owner's call. `ctx/reader-summaries.json` and `ctx/roadmap-notes.json` carry older, looser host descriptions (core count, "about 15 stacks") that predate this round and were not changed.

### Checks run

`go vet` on storekit, topology, authz, httpapi; `go test ./internal/storekit/benchutil ./internal/authz`; `go test ./internal/httpapi -run Fleet`; `node tools/check-contracts.mjs`; `node tools/check-api-reference.mjs`.

### Not run

`packages/security` node tests (need the package build; no node_modules in the worktree, but `check-contracts` cross-checks the RBAC table), Postgres-gated benches, full suites.
