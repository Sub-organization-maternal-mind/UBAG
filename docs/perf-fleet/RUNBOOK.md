# Helper-plane runbook

Operator guide for the shared-fleet helper plane (P4). Written before any canary (P4.20): the metrics, alert rules and procedures here are what a canary is allowed to rely on. Nothing in this file has been exercised against a real helper; the alert rules were checked for structure and metric existence only (see "What is verified"). Never run any step here against the shared VPS without the owner's go-ahead, and never touch production from CI or an agent.

Safe-mode applies throughout: provider logins are out-of-band human logins on the helper, the gateway never types, stores or scrapes credentials, and no procedure below asks for that.

## Flags and the ladder

Each rung needs the previous one. All default off.

| Flag | Rung | Effect when on |
|---|---|---|
| `UBAG_HELPER_NODES` | 1 | Node store (allocations, per-node state, SPKI registry). Enables the per-node metrics. |
| `UBAG_HELPER_PLANE` | 2 | Separate mTLS gRPC listener for helpers. |
| `UBAG_HELPER_DISPATCH` | 3 | The primary dials helpers and runs placed jobs as fenced attempts (P4.14; needs `UBAG_EXECUTOR_ATTEMPTS`, `UBAG_HELPER_CLIENT_CERT_FILE`, `UBAG_HELPER_CLIENT_KEY_FILE`, `UBAG_HELPER_WORKLOAD_VERSION`). Nothing is placed until a picker is wired (P4.17): with the flag on and no picker every job still runs locally. It also turns on the attempt reconciler (P4.18; window `UBAG_HELPER_RECONCILE_WINDOW_SECONDS`, default 600) and stops the stale-job sweep from expiring attempts. |
| `UBAG_HELPER_VOICE` | 4 | Voice media on a helper (not wired yet). |
| `UBAG_EXECUTOR_ATTEMPTS` | - | Attempt ledger (needed by the fenced commit path; Postgres or memory store). |
| `UBAG_EXECUTOR_LEASE_TTL_MS` | - | Queue lease TTL (`0` = legacy no expiry; otherwise 30000 to 900000). |

**Containment, in order of preference:** drain the node (below); unset `UBAG_HELPER_DISPATCH`; unset `UBAG_HELPER_PLANE`; unset `UBAG_HELPER_NODES`. Each unset needs a gateway restart and leaves local execution untouched, because the local worker path never depended on these flags. Do not delete the node tables.

**Before unsetting `UBAG_HELPER_DISPATCH`, drain first.** The reconcile gate goes away with the flag: a job that still has a submitted helper attempt in the ledger would then be run on this gateway like a fresh job, and its prompt would be sent a second time. Stop dispatching (drain every node, or wait until no job has an open attempt) and let in-flight attempts reach an end before the restart.

## Metrics

All on `GET /v1/metrics` (unauthenticated, cross-tenant aggregates; no tenant, job or attempt ids). Source: `apps/gateway/internal/helpermetrics`.

| Series | Type | Labels | Meaning |
|---|---|---|---|
| `ubag_helper_nodes` | gauge | `admission` | Nodes by current admission verdict: `eligible`, `revoked`, `draining`, `reservation_unknown`, `grant_expired`, `heartbeat_missed`, `no_capacity`. Computed at scrape with the same `Evaluate` placement uses. |
| `ubag_helper_node_heartbeat_age_seconds` | gauge | `node_id` | Seconds since the last heartbeat. Absent until the node's first heartbeat. |
| `ubag_helper_node_drain_state` | gauge | `node_id` | Grant state: 0 active, 1 draining, 2 revoked (anything unknown reads 2). |
| `ubag_helper_node_admission_limit` | gauge | `node_id` | Concurrent browser workloads admitted right now; 0 means refused. |
| `ubag_helper_metrics_source_up` | gauge | - | 1 when the node store was readable for this scrape. Absent when `UBAG_HELPER_NODES` is off. |
| `ubag_lease_renew_failures_total` | counter | `lease` (`queue`, `exec`, `attempt`), `reason` (`lost`, `error`) | Failed renewals. `queue` and `exec` are recorded by the local consumer today; `attempt` is recorded by the helper dispatcher once it lands, so it reads 0 until then. |
| `ubag_helper_fenced_rejects_total` | counter | `reason` | Helper writes refused as stale or fenced (`UBAG-WORKER-NODE-FENCED-005`). |
| `ubag_helper_policy_violations_total` | counter | `reason` | Helper streams rejected or failed for scope, content or budget violations. |
| `ubag_helper_reconcile_total` | counter | `action` (`run`, `wait`, `resume`, `fail_closed`), `reason` | Attempt reconcile decisions (P4.18) for leased jobs that already have attempts. Only the pairs the policy can produce exist, all from the first scrape; a `wait` repeats every few seconds while a job is held. |

Counters are per gateway process and reset on restart; use `increase()`. Helper-influenced text never becomes a label value: unknown reasons are folded into `other`. Heartbeat age and admission are derived from the node store at scrape time (the body is cached for 5 s), so they are only as fresh as the store.

Alert rules: `deploy/prometheus/helper-plane-alerts.yaml` (load via `rule_files`, or wrap the groups in a `PrometheusRule`). Each alert's `runbook` annotation points at a section below.

## Procedures

### Heartbeat stale or missing

Alerts: `UBAGHelperHeartbeatStale`, `UBAGHelperHeartbeatMissing`.

Three missed 15 s heartbeats (45 s) stop new placements on the node; running work is not touched. `heartbeat_missed` also covers a node that has never reported.

1. Check `ubag_helper_nodes` for how many nodes are affected. One node: a helper or link problem. All nodes: suspect the gateway side (store, listener) or the WireGuard path.
2. Gateway logs: look for `helper` rejection lines from the trust plane (`OnReject`). A rotated or expired certificate (leaf lifetime is at most 72 h) or an SPKI pin that does not match the registry shows up there.
3. On the helper host: agent running, WireGuard up, clock in sync (certificate validity is enforced).
4. If it does not recover within the grace period, drain the node (see "Drain or revoke stuck") and let in-flight work finish or be reconciled. Do not re-pin SPKI by hand to get a node back without confirming the new certificate came from the manager CA.

### No eligible helper nodes

Alert: `UBAGHelperNoEligibleNodes`.

Nodes are registered but none can take placements. Read `ubag_helper_nodes` by `admission` and follow the dominant reason: `grant_expired` (below), `heartbeat_missed` (above), `draining` or `revoked` (below), `reservation_unknown` (the manager has not stated its reservations; admission stays closed by design), `no_capacity` (pressure hysteresis or a limit of 0: check `ubag_helper_node_admission_limit` and the helper's reported capacity). With no eligible helper, jobs keep running locally; nothing is lost.

### Grant expired (manager unreachable)

Alert: `UBAGHelperGrantExpired`.

A grant is only valid until its `valid_until`. The manager is polled with last-known-good semantics: while it is unreachable nothing is granted or increased, existing grants keep governing until they expire, and after the stale grace every active grant is treated as draining. So this alert means the manager stopped renewing or is unreachable.

1. Confirm manager reachability from the gateway host (the manager is external to this repo; there is no project-facing API to call from UBAG).
2. Do not extend `valid_until` by editing the store. A new grant must come from the manager with a higher generation.
3. Until it recovers there is no new capacity on helpers. That is the intended fail-closed behaviour, not an incident by itself.

### Drain or revoke stuck

Alert: `UBAGHelperDrainStuck`.

Drain stops new attempts and lets in-flight work finish; non-voice attempts may be cancelled only after the grace (default 5 min, max 1 h); voice calls are never cancelled by drain. A node stuck for 30 min is holding work or was never released.

1. `ubag_helper_node_drain_state` 1 = draining, 2 = revoked. Check whether attempts are still running on it (job events carry `data.helper.node_id`).
2. Voice calls in progress will finish on their own; wait or end the call through the normal voice path.
3. A revoked node's registry entry is sticky and a re-admitted helper gets a new node id. Do not try to un-revoke.

### Helper metrics source down

Alert: `UBAGHelperMetricsSourceDown`.

The gateway could not list allocations from the node store for a scrape (Postgres down or migration missing). While this fires the per-node series are absent and the other node alerts cannot fire. Fix the store first (`/v1/ready` shows the store checks), then re-read the other alerts. Do not silence this one while any canary is running.

### Lease renewal failures

Alerts: `UBAGAttemptLeaseRenewFailing`, `UBAGQueueOrExecLeaseRenewFailing`.

- `lease="attempt"`: a helper attempt holder cannot renew its lease. It will be fenced when the lease lapses and the attempt may be re-dispatched. Check the helper link and the primary-to-helper dial before anything else. Any non-zero value during a canary is a stop-and-look condition.
- `lease="queue"`, `reason="lost"`: another consumer took the job after the queue lease lapsed (slow host, stalled worker); this run was cancelled. Repeated hits mean the TTL is too short for the host load or a worker is stalling.
- `lease="queue"` or `"exec"`, `reason="error"`: the spool filesystem or the lease store is failing renewals. Check disk, Postgres and `ubag_gateway_ready`.
- `lease="exec"`, `reason="lost"`: the execution lease token expired and was swept; the run stops to avoid a double submission.

A job whose run was cancelled after the prompt was submitted is not retried blindly; it ends with `reconcile_required` rather than a second submission.

### Fenced writes and policy violations

Alerts: `UBAGHelperFencedWrites`, `UBAGHelperPolicyViolations`.

- Fenced (`UBAG-WORKER-NODE-FENCED-005`): a helper wrote with a superseded lease generation. The ledger refused it and nothing was written; the legitimate holder is unaffected. One-off hits after a reconnect are expected; a steady stream means a helper missed its cancel or a lease is flapping (see lease renewal failures).
- Policy violations: wrong node, tenant, job or attempt, a sequence gap, a disallowed event type, malformed or oversize output. The attempt is failed (`helper_output_limit` or `helper_event_invalid`) or the write is rejected. The audit chain holds one record per reason and session: `attempt.fenced_rejected` and `helper.policy_violation`, actor `node:<id>`, with only ids and fixed reason text. A wrong-scope or wrong-node violation from a certified helper is a security event: revoke the node, then investigate.

### Remote attempt held back, lost or failed for reconcile

Source: `RemoteWorkerRunner` (ADR-0014). Log lines carry `job_id`, `attempt_id` and `node_id`, never tenant data.

- `worker placement refused; retrying the lease after a delay` with `reason=` `helper_unreachable`, `helper_workload_version`, `helper_registry_digest`, `helper_protocol`, `helper_node_mismatch` or `helper_clock_skew`: the picked helper is not usable, so the job was held back before anything was leased (no ledger row, nothing submitted). Fix the helper (version, adapter registry, clock, link) or drain it; the job retries every few seconds meanwhile. A steady stream for one node means the picker keeps choosing a node the primary will not use.
- `reason=attempt_lease_held` or `attempt_conflict`: a previous attempt of the job still holds its 120 s ledger lease (a refusal or a lost helper leaves it to lapse). The job waits for the lapse; this is normal for up to two minutes after a helper loss.
- `reason=helper_lost`: the helper was refused, unreachable or fenced before the prompt was submitted. The job goes back to the queue and gets generation n+1 after the lease lapses. `reason=attempt_lease_lost`: the ledger gave the attempt to someone else; nothing was written.
- A job `failed_terminal` with `reconcile_required`, `submitted: true` and a `helper lost after prompt submission` log line: the helper (or the primary) was lost after the prompt left. It is never replayed. Look at the provider's own conversation for the turn; if it is there, the answer has to be collected by hand. A job whose gateway was restarted (rather than its helper lost) is settled by the attempt reconciler, which collects a finished answer from the helper by itself: see the next section.
- `helper_output_limit` / `helper_event_invalid` on a job: the helper broke the output contract and the attempt was failed (see "Fenced writes and policy violations").
- A cancel reaches a remote attempt within about a second through the in-process hint; a cancel written by another gateway process takes up to 2 s. If `CancelAttempt` does not land (the helper is down) the helper's own lease expiry stops the attempt, at most 110 s later.
- Jobs with declared attachments, a conversation, a `voice.*` command type or an `antigravity_*` target are never dispatched; they run on the primary.

### Attempt reconcile after a restart or helper loss

Alert: `UBAGHelperReconcileFailedClosed`. Source: the reconcile gate in the worker consumer and `nodes.Reconciler` (ADR-0015). Log lines start `attempt reconcile:` and carry `job_id`, `attempt_id`, `node_id`, `generation` and `reason`. Counter: `ubag_helper_reconcile_total{action,reason}`.

Every leased job is judged against the attempt ledger before it is placed or run, whichever queue it came from (a spool lease recovered after a restart, a TTL reclaim, a NATS redelivery). The ledger decides first; the helper is asked (`InspectAttempt`, read-only) only when the answer depends on it. The manager is never asked: the node store's last accepted endpoint and revocation are all it reads, so a manager outage changes nothing here. A job whose prompt may already have left is never started again.

| `action` | `reason` | Meaning |
|---|---|---|
| `run` | `no_attempt`, `attempt_ended`, `attempt_lapsed` | Nothing live and nothing submitted: the job is placed or run as usual. After a lost attempt that never submitted, the next attempt takes generation + 1 and the old holder is fenced. |
| `wait` | `attempt_lease_held` | The last attempt's 120 s lease (plus a 15 s margin for the helper's kill grace) has not lapsed: the job is held and retried every 10 s at most. Up to about two minutes after a gateway restart is normal. |
| `wait` | `helper_unreachable`, `helper_still_running` | A submitted attempt whose helper does not answer, held until the window ends (`UBAG_HELPER_RECONCILE_WINDOW_SECONDS`, default 600 s, counted from the lapse of its lease); or an unsubmitted one the helper has not stopped. |
| `resume` | `helper_holds_attempt`, `helper_finished`, `helper_saw_submit` | The helper holds the submitted attempt. Its lease is taken over (ledger first, then helper), and when it has ended the outcome is committed as its terminal event through the fenced ingest. The job completes (or fails with the helper's own failure) as if it had never been interrupted. `helper_saw_submit`: only the helper saw the prompt leave; the ledger is updated first. |
| `fail_closed` | see below | The prompt was submitted and nothing can settle it. The job ends `failed_terminal` with `submitted: true` and `reconcile_required: true` (event `reason` = `attempt_reconcile_<reason>`), its queue lease fails and is never retried, and it is never run again. |

`fail_closed` reasons:

- `helper_no_record`: the helper restarted and forgot the attempt. Check the helper's uptime and logs. The prompt may be in the provider's conversation.
- `helper_unreachable`: the helper stayed unreachable for the whole window. Fix the link or the node; conversations bound to the node are marked broken.
- `node_revoked`: the node was revoked; it is not dialed. Conversations bound to it are marked broken.
- `stale_helper_result`: the helper answered for another lease generation or another input than the ledger's. Nothing from the answer was used. Treat it like a fenced write (see above) and investigate the node: it is either buggy or confused, and a repeat is a reason to revoke it.
- `attempt_fenced`: the attempt was already expired (or finished) in the ledger, so nothing the helper holds can be committed. Usually an attempt left by a gateway that ran with another configuration.
- `helper_not_stopping`: an attempt that never submitted is still running on a helper long after its lease. The helper's lease logic is not working: investigate the node.

For every `fail_closed` job: look at the provider's own conversation for the turn. Re-send it by hand only when the turn is not there; the gateway will not.

Limits to know:

- A resumed attempt commits only its outcome. The provisional events between the crash and the end (tokens) are not replayed, so the job's event history has a gap there; the result is complete.
- Resume never calls `RunAttempt`. A helper that lost the attempt cannot be made to start it again by a reconcile.
- If a helper dies after the prompt left and before any event reached the primary, and nothing can be asked of it any more, the ledger still says "not submitted": the job is reassigned. This window is the width of one event delivery; a durable helper journal would close it (ADR-0015).
- A graceful gateway shutdown with a submitted attempt in flight still cancels the helper and fails the job closed (P4.14). Only a crash or a kill is resumed.

## Pre-canary checklist (P4.20 is still external-blocked)

A canary has not run and this runbook does not claim one. Before one is attempted, all of the following are the operator's to confirm:

- [ ] Alert rules loaded in the production Prometheus and visible as healthy; each alert's expression returns a series (or an empty result you understand) in the Prometheus UI.
- [ ] `ubag_helper_metrics_source_up` is 1 and `ubag_helper_nodes` sums to the number of nodes the manager granted.
- [ ] The canary node's `ubag_helper_node_heartbeat_age_seconds` stays under 45 s for at least 10 minutes.
- [ ] A drain of the canary node was rehearsed and `ubag_helper_node_drain_state` moved to 1.
- [ ] The containment order above is written down where the person on call will look, with the restart procedure.
- [ ] `ubag_lease_renew_failures_total{lease="attempt"}`, fenced rejects and policy violations are all flat at the start.
- [ ] The manager-down behaviour is understood: no new grants, no increases, no new placements beyond granted limits.

## What is verified

Checked by tests or targeted checks in this slice: the metric renderer (bounded labels, per-node gauges from a memory node store, source failure visible), the executor's fenced-reject counter on a real fenced commit, the `/v1/metrics` wiring, that every metric named in the alert file is emitted by the gateway (`tools/check-alert-metrics.mjs`), and the alert file structure and runbook anchors (`tools/check-helper-alerts.mjs`).

The attempt reconcile (P4.18) was checked against a fake helper and the real ledger (memory store) and against the real helper service over real mTLS on loopback: collect a finished attempt after a lost primary, adopt a running one and keep it alive past its original lease, a helper that forgot the attempt, a stale generation, an unreachable helper through and past the window, lost before submission reassigned at generation + 1, and the manager-down case. The chaos experiments for it are definitions only (`tests/chaos/experiments/reconcile-*.json`).

Not verified: `promtool check rules` (not available here), the expressions against live data, the Postgres node store under scrape load, the Postgres attempt ledger under the reconciler, any real helper, the manager, or a canary. The `lease="attempt"` series is produced by the remote runner's renewal loop (P4.14); it has only been exercised against a fake helper and a loopback helper service, never a real node.
