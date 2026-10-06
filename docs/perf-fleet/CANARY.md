# Helper canary runbook (P4.20, EXTERNAL-BLOCKED)

Status: **not run.** This is the checklist and drill script for the first live canary with one helper. It was written without a real manager, a real helper, WireGuard or any production access, so nothing in it has been exercised against live infrastructure. Do not record a canary as done until the evidence section is filled in with real output by a human operator.

Rules that hold throughout: never dispatch anything against the shared VPS without the owner's go-ahead; nothing here is run from CI or by an agent; secrets, certificates and env values are never pasted into this repo or a PR; safe-mode applies (provider logins on the helper are human, out of band; no step types, stores or scrapes a credential). The canary target is the `mock` adapter, so no provider account is touched.

## What is built, and what is not

| Piece | State |
|---|---|
| Grant polling with ETag and last-known-good (`nodes.Poller`, started by `serve` when `UBAG_FLEET_MANAGER_URL` is set) | Built (P4.15). No auth on the request: the manager's scheme is undefined. |
| Helper image and workload manifest | Built, inert (P4.19). The image has never been built in CI; dispatch `helper-image.yml` with `push: false` first. |
| Placement, probing, ceiling, ramp, reconcile | Built (P4.17, P4.18), behind `UBAG_HELPER_DISPATCH`. |
| Rehearsal tooling | `tools/fake-fleet-manager.mjs`, `tools/scan-env-dump.mjs`, `tools/check-fleet-canary.mjs` (this slice). |
| Real manager allocation output | **External.** The OET manager has no project-facing allocation API yet. |
| Manager-issued certificates, WireGuard, firewall, Docker limits | **External** (the manager and the host owner). |
| Operator route to bind a tenant profile to a node | **Missing** (P4.16 follow-up). Without it nothing is ever placed: every job runs locally. The canary needs this, or a one-off supervised call to `ProfileStore.Bind`. Decide which before starting. |
| Auth on the manager poll | **Undefined.** Agree mTLS-over-WireGuard or bearer with the manager team; `NewHTTPSource` takes an injectable `http.Client`, but `serve` passes none today. |

## Preconditions

All must be true before step 1. Anything unchecked means stop.

- [ ] The owner approved a canary window and named who is on call.
- [ ] `docs/perf-fleet/RUNBOOK.md` pre-canary checklist is complete (alerts loaded, metrics source up, drain rehearsed, containment order written down).
- [ ] Rehearsal passed on a laptop: `node tools/check-fleet-canary.mjs` is green, and the drills below were walked once against `tools/fake-fleet-manager.mjs` with a local helper (loopback addresses), so the operator has seen each log line and metric move.
- [ ] The manager team has produced a real `allocation_list` for one node and it parses: save the response body to a file and run it through a gateway in a scratch environment (never prod) with `UBAG_FLEET_MANAGER_URL` pointing at it; the poll must log no `untrusted manager response`. Required: `cert_identity.uri_san` is exactly `spiffe://ubag/node/<node_id>` (the fixture form `spiffe://fleet.example/...` is rejected), `reservation_state` is `known`, `max_browser_workloads` is `1`, `state` is `active`, `valid_until` is in the future.
- [ ] The helper image was built by the manual workflow and its tag is `sha-<commit>` of the commit the primary runs. `UBAG_HELPER_WORKLOAD_VERSION` on the primary equals the manifest's `workload_version`.
- [ ] The manager applied limits to the helper container (the manifest has requests only) and the firewall allows only the primary to reach the WireGuard address on port 7443. CDP, VNC and noVNC are bound to loopback on the helper and not published.
- [ ] Manager-issued certificates are in place: helper node cert (at most 72 h, URI SAN carries the node id), the primary's client cert (`spiffe://ubag/primary/<id>`), and the manager CA bundle on both sides. Note the expiry time; the canary must finish well inside it.
- [ ] The gateway has `UBAG_EXECUTOR_ATTEMPTS=true`, Postgres store, and the ladder flags set in a way that can be reverted by unsetting (`UBAG_HELPER_NODES`, `UBAG_HELPER_PLANE`, `UBAG_HELPER_DISPATCH`, `UBAG_FLEET_MANAGER_URL`).
- [ ] A canary tenant exists and a profile for the `mock` target is bound to the canary node (see the table above for how). No other tenant has a binding on that node.

## Step 1: join at one workload

1. Set the ladder flags on the primary and restart it. Local execution is unaffected.
2. Watch `ubag_helper_nodes{admission}`: the node must go to `eligible` and `ubag_helper_node_admission_limit` must read 1 (new helper starts at 1 browser workload; the ramp adds more only after healthy time, not for the canary). `ubag_helper_node_heartbeat_age_seconds` must stay under 45 s for 10 minutes.
3. Submit one job for the canary tenant with target `mock`. It must complete, its events must carry the attempt id (`data.attempt_id`) and must NOT carry any node id (tenants never see fleet placement), and `ubag_helper_placements_total` must show one remote placement.
4. Read the canary tenant's audit chain: it must hold one `attempt.granted` (appended before the lease; an unwritable chain holds the job back with reason `audit_unavailable`) and one `attempt.committed` for the job. `asset.token_issued` and `viewer.opened` are not emitted yet (no helper asset-token or viewer path exists; D3), so they are not graduation criteria.
5. Run the env dump scan on the helper container (see Evidence): it must find no secrets.

Pass: job completed on the helper, heartbeat flat, `ubag_lease_renew_failures_total{lease="attempt"}`, fenced rejects and policy violations all unchanged from the start.

## Drill A: kill the helper before the prompt is submitted

Goal: the job is reassigned at generation + 1 and the old holder is fenced.

1. Submit a `mock` job that is held at the helper before submission (use a mock option that delays the prompt; if the mock cannot, kill the helper container within the placement window and accept that the result may land in Drill B instead; record which one happened).
2. Kill the helper container (or drop the WireGuard link). Do not touch the primary.
3. Expect: `attempt reconcile:` log with `action=wait reason=attempt_lease_held` for up to about 135 s, then `action=run reason=attempt_lapsed`; the job runs again (locally, since no node is eligible, or on the helper after it returns) at generation + 1.
4. Bring the helper back. A late write from the old holder must be rejected: `UBAG-WORKER-NODE-FENCED-005` counted in `ubag_helper_fenced_rejects_total`, nothing written.

Pass: exactly one terminal result for the job, no duplicate prompt visible in the mock's transcript.

## Drill B: kill the helper after the prompt is submitted

Goal: reconciled or failed closed, never submitted twice.

1. Submit a long `mock` job and kill the helper after the `prompt_submitted` event is visible.
2. Restart the primary during the outage if you want the restart path (the reconcile gate reads the ledger at the consumer).
3. Expect: `action=wait reason=helper_unreachable` until `UBAG_HELPER_RECONCILE_WINDOW_SECONDS` (default 600) from the lease lapse, then `fail_closed reason=helper_unreachable`: the job ends `failed_terminal` with `submitted: true` and `reconcile_required: true`, and is never run again. If the helper comes back inside the window with the attempt, expect `resume` and the helper's own outcome. A helper that came back having forgotten the attempt gives `fail_closed reason=helper_no_record`.
4. `UBAGHelperReconcileFailedClosed` may fire; that is expected here.

Pass: the mock transcript shows the prompt once; the job's terminal state matches one of the outcomes above.

## Drill C: manager down

Goal: in-flight work continues and nothing new is granted.

1. With a job running on the helper, make the manager unreachable (block the poll route, or stop the manager). With the rehearsal tool the equivalent is `POST /_control {"mode":"down"}`; the real drill must use the real manager or a firewall rule on the primary, not the fake.
2. The running job must finish normally. New jobs for the canary tenant keep placing on the helper only inside the already-granted limit of 1; no increase appears. After `UBAG_FLEET_GRANT_STALE_GRACE_SECONDS` (default 600) without a good poll the grant reads `draining`: new jobs run locally and nothing is lost.
3. Restore the manager. The next good poll restores `active` and the node is eligible again after its heartbeat. A grant raise published during the outage must not take effect until then.

Pass: no job lost or failed by the outage, no placement above the granted limit, `ubag_helper_nodes` shows the `draining` transition and its recovery.

## Drill D: drain

1. Ask the manager to publish `state: draining` for the node (rehearsal: `POST /_control {"state":"draining"}`).
2. `ubag_helper_node_drain_state` must read 1, new jobs run locally, in-flight work finishes.
3. Rehearse containment once on a scratch gateway: unset `UBAG_HELPER_DISPATCH` only after draining (see RUNBOOK "Flags and the ladder").

Pass: no placements after the drain, in-flight attempt finished or reconciled.

## Evidence

Record in the PR or a private ticket, never raw output with secrets. Per drill: UTC time, the commands or console actions taken, the relevant log lines (job id, attempt id, node id, generation, reason only), the metric values before and after, and the job's terminal state. The env scan is:

```
docker exec <helper-container> env | node tools/scan-env-dump.mjs
docker exec <primary-container> env | node tools/scan-env-dump.mjs --allow UBAG_VOICE_RELAY_SECRET
```

The scanner prints variable names only, never values. A finding on the helper is a failure of the exit gate. The primary legitimately holds secrets (store DSN, app secrets); use `--allow` only for the named, known ones, and treat anything else as a finding.

Exit gate (P4.20): a mock-target job ran end to end on a real helper; the kill drills reassigned before submission and reconciled after; the manager-down drill kept in-flight work and granted nothing new; the env scan showed no secrets. Until a human has produced that evidence, the status of P4.20 stays external-blocked.

## Abort

At any sign of trouble, in this order: drain the node (manager `state: draining`); unset `UBAG_HELPER_DISPATCH` after in-flight attempts finish; unset `UBAG_HELPER_PLANE`; unset `UBAG_HELPER_NODES`. Each unset needs a restart and leaves local execution untouched. Do not delete node tables and do not edit `valid_until` or generations by hand. Abort immediately on: any `lease="attempt"` renewal failure that does not recover, a steady stream of fenced writes, a policy violation, a certificate near expiry, or any secret in the scan.

## Rehearsal tooling

```
node tools/fake-fleet-manager.mjs --node-id canary-1 --endpoint 127.0.0.1:7443 --port 8099
UBAG_HELPER_NODES=true UBAG_FLEET_MANAGER_URL=http://127.0.0.1:8099/v1/allocations ...   # gateway under test
curl -X POST http://127.0.0.1:8099/_control -d '{"mode":"down"}'    # ok | down | bad ; or {"state":"draining"}, {"max_browser_workloads":2}
```

The fake binds loopback only, has no auth and no TLS, and is refused on any other host. It is a rehearsal aid, not evidence: a drill against it never satisfies the exit gate.
