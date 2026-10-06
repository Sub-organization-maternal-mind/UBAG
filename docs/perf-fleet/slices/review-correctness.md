# Review (correctness lens): fixes

Branch `feat/pf-review-correctness`, base `feat/perf-fleet`. All eight findings were re-verified against the code and were real; each was fixed minimally. No new flags, no migrations.

## Fixed

1. **P1.3 gRPC StreamJobEvents stalls 30 s on `blocked` / retryable failure.** `isTerminalEvent` now also asks the store vocabulary through the new `jobs.EventEndsJob(type, data)` (same mapping as `workerEventStatus`: `blocked`, `failed`, `failed_retryable` are terminal). No per-batch `Get` is restored. Test: `TestStreamJobEventsClosesPromptlyOnBlockedAndRetryableFailure` (idle check set to 1 h, so only a prompt close passes).
2. **P3.13 `sseTerminalEvent` ignored retryable failures and `blocked`.** It now delegates to `jobs.EventEndsJob`, so close-on-terminal and the 204 resume work for those ends. Test: `TestSSECloseOnTerminalForRetryableAndBlocked` (`failed` retryable, `failed_retryable`, `blocked`).
3. **P3.13 SSE did not implement the merged contract (landed on feat/perf-fleet concurrently by the security review; this branch kept that side on rebase).** The handler reads `after_sequence` (400 on a malformed or negative value, code `UBAG-VALIDATION-EVENT-SEQUENCE-001`), uses the larger of it and the Last-Event-ID sequence, and returns 204 when the cursor is at or past the end of a terminal job regardless of `UBAG_SSE_CLOSE_ON_TERMINAL`. The stale "until P3.13 ... ignores" paragraph in `openapi.yaml` is removed and the Terminal bullet names `failed`/`failed_retryable`/`blocked`. The test that pinned the opposite behaviour is replaced by `TestSSEResumePastTerminalReturns204WhenFlagOff`; `TestSSEAfterSequenceContract` covers skip, larger-cursor-wins, 204 and 400. The conformance fixture `sse-resume.json` is not driven against the handler; the new Go tests mirror its cases (follow-up).
4. **P3.7 `normalizedEvents` allocated before the stale drop.** Allocation now follows `dropStaleAttemptEvents`. Test: `TestRunOnceDropsStaleAttemptEventWithoutZeroTail` (RunOnce level, flag on).
5. **P3.2 post-submit drift / manual action reported as retryable `blocked`.** When the prompt was submitted (streaming path always; buffered path with `UBAG_WORKER_STRICT_SUBMIT`), drift and `ManualActionRequired` now end `failed_terminal` with `submitted`, `reconcile_required`, `retryable:false` and the original reason (`selector_drift_detected` or the manual-action reason), per D4. The operator-facing `session.manual_action_required` event is still emitted. AIMD signals are unchanged. Flag off, buffered path: unchanged. Tests: `test_post_submit_drift_is_terminal_with_reconcile_marker`, `test_post_submit_drift_flag_off_stays_blocked`, and the streaming test renamed to `test_post_submit_drift_is_failed_terminal_not_retried`.
6. **P0.2 `UBAG_ADMISSION_SHARED` not deliverable in `docker-compose.vps.yml`.** The `UBAG_ADMISSION_SHARED` passthrough (default `true`) and the FLAGS.md Compose cell landed on `feat/perf-fleet` concurrently (kept on rebase); this branch adds the three `UBAG_ADMISSION_MAX_INFLIGHT_*` caps (empty default = unlimited) that the env example documents. `node tools/flag-graduation-check.mjs` passes.
7. **P6.6 worker-only flags not forwarded.** `UBAG_WARM_RESUME_FASTPATH` and `UBAG_PROFILE_OPTIONS_POLICY` are now in `workerdaemon.AllowedEnv` (the same allowlist entries landed on `feat/perf-fleet` concurrently; the rebase kept that side). New test `TestWorkerReadFlagsAreForwardedOrExplicitlyExempt` scans `apps/worker/ubag_worker` for quoted `UBAG_*` names and requires each to be forwarded or listed (with a reason) in `notForwarded`, so a new worker-read flag cannot be forgotten silently. The compose passthrough for these two flags is deliberately not added (they stay `none` in FLAGS.md until their rollout step); the flag-graduation checker was not extended, the Go test is the guard.
8. **P3.6 pool-saturation hold never noted.** The overload branch now calls `HoldBoard.NoteAssigned` with the refusal reason. The job is already `assigned` by then, so the hold is shown to operators (`HoldBoard.AllCounts` feeds the fleet summary) and is excluded from the tenant's `Counts` (queued-by-reason), which keeps `queued_by_reason` consistent with the `queued` count. Test: `TestDaemonPoolOverloadNotesAnAssignedHold`.

## Not changed (partial on finding 8)

- A tenant still reads such a job as `assigned` with no `queue_reason`: the job-response schema defines `queue_reason` for `queued` only, and showing it on `assigned` is a contract change. Follow-up if wanted.
- Queue wait is still observed on the first lease only. The extra pool wait of a retried assigned job is not added to `ObserveQueueWait`; observing it on each retry would double count.

## Checks run

- `go test ./internal/jobs ./internal/grpcapi ./internal/serve ./internal/workerdaemon` pass; `go test ./internal/httpapi -run SSE`, `./internal/executor` (DaemonPool, Hold, Attempt, Queue) pass.
- `python -m pytest tests/test_strict_submit.py tests/test_stream_events.py` (26 pass).
- `node tools/flag-graduation-check.mjs`, `node tools/check-contracts.mjs`, `node tools/check-api-reference.mjs` pass.

## Checks not run / known failures

- Full suites, docker, postgres-gated tests.
- In full package runs, `httpapi TestAntigravityLoginRelaysCodeToOwnedWorker` fails with "isolated account worker is unavailable" (needs the isolated account worker, unrelated to these changes; untouched code path) and `executor TestDaemonPoolRoutesToTheWarmSlotAndEvictsTheLeastRecentlyUsed` failed once under load but passes in isolation (real-process timing).

## Follow-ups

- Drive `packages/conformance/fixtures/streaming/sse-resume.json` against the handler in a Go test.
- Decide whether `queue_reason` should cover assigned-but-held jobs (contract change).

## Round 2 (P1.8 and P5.5 findings), branch `feat/pf-review-correctness-2`

Three findings re-verified: two fixed, one already resolved upstream. No new flags, no migrations.

### Fixed
1. **P1.8 headline hypothesis contradicted by its own benches; admission lane shape wrong (medium).** Confirmed: `AcquireToken` locks every lane in its loop (the shard said "per dynamic lane"), production runs one lane (tenant/global lanes only exist when `UBAG_ADMISSION_MAX_INFLIGHT_*` is set, and `docker-compose.vps.yml` leaves them empty), and the three-lane bench roughly doubled the measured cost. `BenchmarkPostgresAdmissionAcquireRelease` now defaults to one dynamic lane (`UBAG_BENCH_ADMISSION_EXTRA_LANES=1` restores tenant+global). New `BenchmarkPostgresCreatePathComposite` (`storekit/benchutil/composite_bench_test.go`) with `UBAG_BENCH_SKIP=audit,idempotency,admission,create` toggles. `docs/perf-fleet/slices/P1.8.md`: table row corrected, the admission lane lock is now the leading hypothesis, the reviewer's non-authoritative numbers are recorded as reviewer-reported, and audit-append decoupling is no longer recommended until a composite A/B shows a gain.
2. **P1.8 runbook probe `grep -c pg_advisory` can never match (medium).** Confirmed: goroutine dumps hold function names, not SQL. The runbook now greps `audit.(*PostgresStore).Append`, `topology.(*SQLTokenBackend).AcquireToken` and `database/sql.(*DB).conn` (pool waiters) and offers the `pg_stat_activity` advisory wait-event query.

### Skipped
3. **P5.5 live voice-session create answers an undocumented 503 when no relay is configured (medium).** Real when reported, but already resolved on `feat/perf-fleet` by the rules review before this round landed: the refusal is gated behind `UBAG_VOICE_LANE_EXCLUSION` (default off, so production's current posture is unchanged) and the 503 is documented on `createVoiceSession` in OpenAPI. That is the reviewer's second suggested option; my competing 501 change was dropped on rebase.

### Checks
- Run: `go vet ./internal/storekit/... ./internal/topology`.
- Not run: any benchmark (needs Postgres), full suites.
