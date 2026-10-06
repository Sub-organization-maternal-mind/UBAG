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
