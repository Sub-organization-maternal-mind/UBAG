# Helper-plane runbook

Operator guide for the shared-fleet helper plane (P4). Written before any canary (P4.20): the metrics, alert rules and procedures here are what a canary is allowed to rely on. Nothing in this file has been exercised against a real helper; the alert rules were checked for structure and metric existence only (see "What is verified"). Never run any step here against the shared VPS without the owner's go-ahead, and never touch production from CI or an agent.

Safe-mode applies throughout: provider logins are out-of-band human logins on the helper, the gateway never types, stores or scrapes credentials, and no procedure below asks for that.

## Flags and the ladder

Each rung needs the previous one. All default off.

| Flag | Rung | Effect when on |
|---|---|---|
| `UBAG_HELPER_NODES` | 1 | Node store (allocations, per-node state, SPKI registry). Enables the per-node metrics. |
| `UBAG_HELPER_PLANE` | 2 | Separate mTLS gRPC listener for helpers. |
| `UBAG_HELPER_DISPATCH` | 3 | The primary dials helpers and runs placed jobs as fenced attempts (P4.14; needs `UBAG_EXECUTOR_ATTEMPTS`, `UBAG_HELPER_CLIENT_CERT_FILE`, `UBAG_HELPER_CLIENT_KEY_FILE`, `UBAG_HELPER_WORKLOAD_VERSION`). Nothing is placed until a picker is wired (P4.17): with the flag on and no picker every job still runs locally. |
| `UBAG_HELPER_VOICE` | 4 | Voice media on a helper (not wired yet). |
| `UBAG_EXECUTOR_ATTEMPTS` | - | Attempt ledger (needed by the fenced commit path; Postgres or memory store). |
| `UBAG_EXECUTOR_LEASE_TTL_MS` | - | Queue lease TTL (`0` = legacy no expiry; otherwise 30000 to 900000). |

**Containment, in order of preference:** drain the node (below); unset `UBAG_HELPER_DISPATCH`; unset `UBAG_HELPER_PLANE`; unset `UBAG_HELPER_NODES`. Each unset needs a gateway restart and leaves local execution untouched, because the local worker path never depended on these flags. Do not delete the node tables.

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
- A job `failed_terminal` with `reconcile_required`, `submitted: true` and a `helper lost after prompt submission` log line: the helper (or the primary) was lost after the prompt left. It is never replayed. Look at the provider's own conversation for the turn; if it is there, the answer has to be collected by hand until the reconciler (P4.18) lands.
- `helper_output_limit` / `helper_event_invalid` on a job: the helper broke the output contract and the attempt was failed (see "Fenced writes and policy violations").
- A cancel reaches a remote attempt within about a second through the in-process hint; a cancel written by another gateway process takes up to 2 s. If `CancelAttempt` does not land (the helper is down) the helper's own lease expiry stops the attempt, at most 110 s later.
- Jobs with declared attachments, a conversation, a `voice.*` command type or an `antigravity_*` target are never dispatched; they run on the primary.

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

Not verified: `promtool check rules` (not available here), the expressions against live data, the Postgres node store under scrape load, any real helper, the manager, or a canary. The `lease="attempt"` series is produced by the remote runner's renewal loop (P4.14); it has only been exercised against a fake helper and a loopback helper service, never a real node.
