# Flag graduation and rollout runbook

How each perf + shared-fleet flag moves from "built, inert" to "on in production", and how it is rolled back. Companion files: `FLAGS.md` (the inventory, machine-checked), `RUNBOOK.md` (helper plane operations and alerts), `CANARY.md` (the helper canary drill), `docs/benchmarks/capacity-report.md` (what was and was not measured), `slices/*.md` (what each slice built).

**Nothing in this document has been executed against production.** No flag is on because of it. It was written without production access, a lab host, a real helper or a live provider session. Every threshold below is a proposal the owner may tighten, not a measured number; the capacity report states that no authoritative capacity number exists. Do not record a flag as graduated until the ledger entry (below) holds real evidence from a human operator.

Rules that hold for every flag: the owner decides and performs every production change; no agent flips a flag, deploys, or connects to the VPS; secrets, env values and raw probe output never go into this repo or a PR; safe-mode applies (user-owned sessions, no automated login, no credential handling, no CAPTCHA solving, probes are read-only and never run while an operator login or viewer session is active).

## 1. Prerequisite: the flag must reach the container

`docker-compose.vps.yml` lists its environment explicitly and has no `env_file`. A flag whose `Compose` column in `FLAGS.md` says `none` is **not delivered to the gateway container**, so setting it in `deploy/vps/env.local` does nothing. Before graduating such a flag, land a separate reviewed change that adds a `NAME: ${NAME:-}` line for the flag (empty default, which is today's behaviour) to the gateway service, then change the row to `empty` in `FLAGS.md`; `node tools/flag-graduation-check.mjs` fails until both agree. Worker-side flags in the daemon's environment allowlist (`apps/gateway/internal/workerdaemon/env.go`: `UBAG_WORKER_STREAM_EVENTS`, `UBAG_WORKER_STRICT_STREAM_END`, `UBAG_WORKER_STRICT_SUBMIT`, `UBAG_WORKER_STAGE_TIMINGS`, `UBAG_WORKER_IDENTITY_LOCK` and others) are forwarded from the gateway container's environment to the worker, so they also need the compose line. Worker flags outside that allowlist (`UBAG_PROFILE_OPTIONS_POLICY`, `UBAG_WARM_RESUME_FASTPATH`) are not forwarded to a gateway-spawned worker today either: check the allowlist in the same change.

## 2. The procedure every flag follows

Each per-flag section below fills in the four labelled steps; this section is the shared frame.

1. **Preconditions.** The previous rung of its pairing is on and stable (section 3), the flag's code default is still `off` in `FLAGS.md` and matches the code, and `node tools/flag-graduation-check.mjs` passes on the commit that production runs.
2. **Live-DOM verification.** Only for flags that change what the worker does on a provider page. A human with their own signed-in session runs, in order: `pnpm check:provider-selectors` (static cross-file consistency), `node tools/provider-refresh/provider-probe.mjs <provider_id>` (read-only capture of the live page; it never types, submits or logs in), and where the flag touches provider settings `node tools/provider-refresh/verify-settings.mjs <provider_id>` (opens and closes the same menus every job opens). Read the capture: no `selector_drift`, composer found, no login wall. Selectors are brittle (CLAUDE.md): a capture older than the provider's last UI change does not count, so re-run it on the day of the canary. Flags with no provider page involved say "not applicable" and name the evidence that replaces it.
3. **Canary.** Stage C0, mock or synthetic target (`tools/synthetic-provider/`), flag on, a short run, to prove the plumbing; stage C1, one real provider and one tenant, a human watching, a handful of jobs; stage C2, all traffic for that flag, with a 24-hour soak before the next flag is touched. Change one flag at a time. The stop conditions common to every flag: any job stuck non-terminal past its deadline, any duplicate result for one job, any cross-tenant read, any prompt submitted twice, any rise in `ubag_lease_renew_failures_total`, a gateway restart loop, or memory over the container limit. A stop condition means roll back first, investigate second.
4. **Rollback.** The owner removes the flag from `deploy/vps/env.local` (or sets the value named in the section) and recreates the gateway container through the normal deploy path. Rolling back is always "back to the code default", which is today's behaviour, and needs no migration. Each section says what a rollback does not undo.
5. **Ledger.** One entry per change, appended by the owner to the live ledger in `PROGRESS.md` (section 6 has the template): flag, value, date, production sha, stage reached, evidence location, who decided, and the rollback that was rehearsed. A flag with no ledger entry is off.

## 3. Order and pairing

Pairs are not optional; each member of a pair is unsafe or pointless alone.

| Ladder | Order | Why |
|---|---|---|
| Strict submission | `UBAG_WORKER_STRICT_SUBMIT` first, then `UBAG_WORKER_STRICT_STREAM_END` | Strict submit stops an interrupted job being replayed after its prompt went out; strict stream end only changes how a deadline-cut answer is labelled. |
| Streaming | `UBAG_WORKER_ATTEMPT_EVENT_IDS`, then `UBAG_WORKER_STREAM_EVENTS` and `UBAG_WORKER_STREAM_INGEST` together with `UBAG_WORKER_STRICT_SUBMIT` already on | Without attempt-scoped ids a re-leased job re-emits ids that collide with the abandoned attempt's events. Streaming implies them in code, but graduate the ids first so the mixed-id window is observed on its own. |
| Event wake | `UBAG_EVENT_NOTIFY=local`, then optionally `UBAG_SSE_CLOSE_ON_TERMINAL`, then a non-zero `UBAG_SSE_MAX_STREAMS` | Independent of the worker ladders; single-replica only (`local`). |
| Worker pool | `UBAG_WORKER_POOL_SIZE` above 1 only after the streaming ladder is settled, and only up to a measured ceiling | The default ceiling of 3 is a placeholder; no slot cost was measured (P3.6). |
| Helper plane | `UBAG_EXECUTOR_ATTEMPTS`, `UBAG_HELPER_NODES`, `UBAG_HELPER_PLANE`, `UBAG_HELPER_DISPATCH`, then `UBAG_HELPER_VOICE`, with `UBAG_FLEET_MANAGER_URL` beside the nodes rung | The ladder is in `RUNBOOK.md`; the canary is `CANARY.md` and is external-blocked. |

Independent flags, any time after their own checklist: `UBAG_FILESPOOL_HONOR_NOT_BEFORE`, `UBAG_REDACT_REMOTE_ENDPOINT`, `UBAG_VOICE_RECONCILER_FAIL_CLOSED`, `UBAG_WARM_RESUME_FASTPATH`, `UBAG_PROFILE_OPTIONS_POLICY` (the last one carries a re-login cost, see its section).

## 4. Per-flag checklists

### `UBAG_WORKER_STRICT_SUBMIT`

Gateway and worker. Types a failure by whether the prompt had been submitted: before submission it is retryable, after it the job ends `failed_terminal` with `submitted: true` and `reconcile_required`, and is never replayed (D4). Pair with the stream ladder.

- **Live-DOM verification:** the submission marker is the worker's `prompt_submitted` event, emitted after the provider's send control is used. Run `provider-probe.mjs` for each provider that will carry traffic and confirm the composer and send control still resolve; a stale selector makes the marker fire in the wrong place and mislabels jobs.
- **Canary criteria:** C0 on the mock target: kill the worker mid-run before and after submission and confirm the first replays and the second ends `failed_terminal` with `reconcile_required`. C1: 10 real jobs, all `completed`, none with `submitted: true` on a non-terminal state. Proposed C2 gate: zero jobs with `reconcile_required` that the provider's own conversation shows were never sent.
- **Rollback:** unset the flag and recreate the gateway. Post-submit interruptions can be replayed again (the blind resubmit risk of the legacy path returns). Jobs already `failed_terminal` stay terminal.
- **Ledger:** record the flag, the date, and the C1 job ids sampled (ids only).

### `UBAG_WORKER_STRICT_STREAM_END`

Worker only. A stream cut by the deadline ends `timed_out` with `data.partial` and no `result`, instead of `completed` (D4). The P4.9 shard found that the gateway payload policy refused the `token_events` key inside `data.partial` (a key with a `token` segment), which would have failed the job instead of ending it `timed_out`; P8.1 exempts that one exact key (`apps/gateway/internal/payloadpolicy`, with a test), so a production gateway must run a build that includes that change before this flag is set. Confirm with a C0 deadline-cut run that the terminal event is accepted.

- **Live-DOM verification:** the end-of-stream detection reads the provider's streaming indicators. Run `provider-probe.mjs` and `verify-settings.mjs` for the provider and confirm the response container and the "still generating" indicator still match.
- **Canary criteria:** C0: a run forced past its deadline ends `timed_out` with `data.partial` and an empty `result`. C1: normal jobs still end `completed` with the full answer. Proposed C2 gate: zero `completed` jobs whose text is shorter than the provider shows.
- **Rollback:** unset the flag and recreate the gateway (the worker inherits it from the gateway's environment). Deadline-cut streams are reported `completed` again, which is the false-success behaviour this flag exists to remove. Jobs already `timed_out` stay.
- **Ledger:** record the flag, date, the deadline-cut run id and the production sha (which must include the `token_events` exemption).

### `UBAG_WORKER_STREAM_EVENTS`

Worker. The warm daemon yields events as they happen instead of buffering them. Pair with `UBAG_WORKER_STREAM_INGEST` and `UBAG_WORKER_STRICT_SUBMIT`.

- **Live-DOM verification:** token events come from reading the response container repeatedly. `provider-probe.mjs` for each provider; confirm the response selector matches while a human sends a message in the probe window (the probe itself sends nothing).
- **Canary criteria:** C0: with the synthetic provider, a streamed job shows token events before the terminal. C1: one real job per provider shows live tokens and the same final text as the batch path. Proposed C2 gate: event count per job within the P3.4 shard's bound and no gateway-side event rejections in logs.
- **Rollback:** turn off `UBAG_WORKER_STREAM_INGEST` first, then this flag, recreate the gateway. With ingest on and events off the worker buffers its events and nothing is live, which is safe; the reverse order is not tested.
- **Ledger:** record the pairing that was on together with it.

### `UBAG_WORKER_STREAM_INGEST`

Gateway. Applies a warm-daemon job's events as they arrive; the single terminal is held until the worker ends cleanly. A stream that dies, ends without its marker or carries zero or two terminals fails the job. Logs one warning when it runs without strict submit.

- **Live-DOM verification:** not applicable to the gateway code; the live-DOM step of `UBAG_WORKER_STREAM_EVENTS` is the evidence for the worker half.
- **Canary criteria:** C0: killed-mid-stream job fails (never `completed`), a clean stream completes, a retry after a partial stream leaves no duplicate or colliding events. C1: real jobs complete with identical text to the batch path. Proposed C2 gate: no job failed for a missing terminal that a batch run would have completed; `/v1/metrics` shows no store write errors.
- **Rollback:** unset the flag and recreate. Jobs return to the batch path. Events already applied from an abandoned attempt stay in the log under their attempt ids.
- **Ledger:** record the stream byte budget and flush window if they were changed from `FLAGS.md` defaults.

### `UBAG_WORKER_ATTEMPT_EVENT_IDS`

Gateway consumer. Event ids carry the attempt, so a re-leased job cannot collide with an abandoned attempt's events. Streaming already implies it per job. See the P3.7 shard for the behaviour of a retry that spans a deploy where one attempt used legacy ids and the next used attempt ids.

- **Live-DOM verification:** not applicable (gateway-only, no provider page). Evidence: a C0 run that forces one retry and shows distinct event ids per attempt.
- **Canary criteria:** C0 retry run as above; C1: a normal job's event ids carry the attempt id and its SSE stream is unchanged for clients. Proposed C2 gate: no event dedupe drops in the store for retried jobs.
- **Rollback:** unset and recreate. New attempts use legacy ids again; retries that straddle the rollback follow the P3.7 mixed-id rules.
- **Ledger:** record the deploy time, so any job spanning it can be recognised.

### `UBAG_EVENT_NOTIFY`

Gateway. `off` is the legacy 50 ms poll; `local` is the in-process wake hub, single replica only. `postgres` (LISTEN/NOTIFY) is reserved and not built: it logs a warning and stays on the poll, and is pooler-incompatible by design.

- **Live-DOM verification:** not applicable (no provider page). Evidence: SSE latency from `tests/load/acceptance.mjs --scenario events-latency` against a scratch stack, never against production.
- **Canary criteria:** C0: with `local`, an event appears on the stream sooner than the 2 s fallback poll. C1: no missed events across a gateway restart and an SSE reconnect with `Last-Event-ID`. Proposed C2 gate: SSE client count and gateway CPU no worse than the poll baseline on the same host.
- **Rollback:** set `UBAG_EVENT_NOTIFY=off` and recreate. Returns to the 50 ms poll; nothing is lost because the hub only wakes a reader that would have polled anyway.
- **Ledger:** record `UBAG_EVENT_FALLBACK_MS` if changed.

### `UBAG_WORKER_POOL_SIZE`

Gateway. Number of isolated warm-daemon slots (default 1 is today's single daemon). Capped by `UBAG_WORKER_POOL_MAX` (default 3, provisional) and it raises `UBAG_WORKER_CONCURRENCY` to match. One active job per physical session identity; saturation is a delayed retry, never a failure (ADR-0011).

- **Live-DOM verification:** each slot has its own page registry and takes the identity lock. For each provider, confirm with `provider-probe.mjs` that the extra slot's tab is the only tab on its identity and that two slots never share one signed-in session. Do not run while an operator login or viewer is active.
- **Canary criteria:** C0: two jobs on two identities overlap, two jobs on one identity serialise. C1: size 2 on the real host, one provider, watching container memory against the 1300 MiB cgroup. Proposed stop rule: working-set memory above 80 percent of the limit, or any OOM kill, means roll back. There is no measured slot cost, so the owner picks the ceiling from `tools/perf/baseline-matrix.mjs budget` output on a lab host, not from the 3 placeholder.
- **Rollback:** set `UBAG_WORKER_POOL_SIZE=1` and recreate. Orphan slot registries stay on disk (the single daemon does not reap them); they are harmless and are reaped when a larger pool next starts.
- **Ledger:** record the size, the measured memory, and the ceiling used.

### `UBAG_SSE_CLOSE_ON_TERMINAL`

Gateway. The stream ends after the first terminal event; a reconnect with a terminal `Last-Event-ID` gets 204. The value must be the literal `true`. A/B with real EventSource clients first: the dashboard and every SDK that reconnects on close.

- **Live-DOM verification:** not applicable (no provider page).
- **Canary criteria:** C0: dashboard and TypeScript SDK follow a job to its terminal and stop reconnecting. C1: Go and Python SDK streams end cleanly. Proposed C2 gate: no client reports a premature close on a retryable `failed`.
- **Rollback:** unset and recreate. Streams stay open after terminal events again.
- **Ledger:** record which client versions were tested.

### `UBAG_FILESPOOL_HONOR_NOT_BEFORE`

Gateway, file executor mode. Scheduled jobs wait for `not_before`; today they are leased at once.

- **Live-DOM verification:** not applicable.
- **Canary criteria:** C0: a job with a future `not_before` stays queued until due and a due job behind it is leased first. C1: no starvation of ordinary jobs while a scheduled one waits. Proposed C2 gate: no queued job older than its `not_before` plus the poll interval.
- **Rollback:** unset and recreate. Scheduled jobs run immediately again; jobs that were waiting are leased on the next poll.
- **Ledger:** record the first scheduled job id observed to wait.

### `UBAG_REDACT_REMOTE_ENDPOINT`

Gateway. `GET /v1/browser/instances` omits `remote_endpoint`. A helper node's endpoint is withheld regardless of this flag.

- **Live-DOM verification:** not applicable.
- **Canary criteria:** C0: the dashboard and CLI still render the instance list without the field. C1: voice control still works (it reads the store directly, not this response). Proposed C2 gate: no consumer error in the dashboard console.
- **Rollback:** unset and recreate. The field is returned again.
- **Ledger:** record which consumers were checked (dashboard, mobile, SDKs).

### `UBAG_PROFILE_OPTIONS_POLICY`

Worker. `namespaced` ignores caller-supplied profile directories and uses a per-tenant, per-target directory. **Cost:** a provider login lives in the profile directory, and logins are human and out of band; moving to a new directory means the new directory has no signed-in session until a human logs in there or the owner moves the profile. Do not graduate it on a production target without that plan.

- **Live-DOM verification:** after the directory change, run `provider-probe.mjs` for the provider and confirm the tab is signed in (no login wall) before any job is sent.
- **Canary criteria:** C0 on the mock target with two tenants: separate directories. C1: one real provider on a throwaway directory the owner has signed in. Proposed C2 gate: no job fails with a login wall that the legacy directory would have passed.
- **Rollback:** set `legacy` (or unset) and recreate; the old directory and its session are untouched by the flag.
- **Ledger:** record the directory migration plan and who performed the logins.

### `UBAG_VOICE_RECONCILER_FAIL_CLOSED`

Gateway. After 5 consecutive store errors per session, the reconciler stops media instead of failing open. Voice media is unavailable in production today (relay secret unset), so this has no live effect until voice is enabled.

- **Live-DOM verification:** not applicable.
- **Canary criteria:** C0: with a faulting voice store, media stops after 5 errors; with a healthy store nothing changes. Proposed C2 gate: no call stopped while the store was healthy.
- **Rollback:** unset and recreate. Containment of voice as a whole stays `UBAG_VOICE_STORE=disabled` (not applied anywhere by this program).
- **Ledger:** record the voice posture at the time (store kind, relay secret set or not, as set or unset only).

### `UBAG_WARM_RESUME_FASTPATH`

Worker. Skips the new-chat gate on a warm resume. Depends on the page being in the expected state, so it is the most DOM-sensitive flag here.

- **Live-DOM verification:** required. `provider-probe.mjs` and `verify-settings.mjs` per provider on the day of the canary; the resume path must be exercised by a human in a real conversation first.
- **Canary criteria:** C0 on the synthetic provider; C1: ten resumed turns on one provider, every answer in the right conversation. Proposed C2 gate: zero answers appended to the wrong conversation.
- **Rollback:** unset and recreate the gateway. The new-chat gate returns.
- **Ledger:** record the provider set and the probe capture file name.

### `UBAG_EXECUTOR_ATTEMPTS`

Gateway. Enables the attempt ledger (Postgres or memory store; SQLite fails closed). Required by the helper dispatch rung. Alone it turns the ledger on (P4.2 shard); the helper rungs are what use it.

- **Live-DOM verification:** not applicable.
- **Canary criteria:** C0: attempts are recorded for every job and the ledger is empty of open attempts after completion. C1: migration 0022 applied (additive, applied automatically on deploy) and the gateway ready probe green. Proposed C2 gate: zero open attempts older than the lease TTL.
- **Rollback:** unset `UBAG_HELPER_DISPATCH` first if it is on, drain (see `RUNBOOK.md`), then unset this flag and recreate. The table stays; do not drop it.
- **Ledger:** record the migration number applied.

### `UBAG_HELPER_NODES`, `UBAG_HELPER_PLANE`, `UBAG_HELPER_DISPATCH`, `UBAG_HELPER_VOICE`, `UBAG_FLEET_MANAGER_URL`

The shared-fleet ladder. Each rung needs the previous one; the semantics, metrics, alerts and every operational procedure are in `RUNBOOK.md`, and the first live drill is `CANARY.md`, which is external-blocked (no manager allocation API, no real helper, no certificates, no operator route to bind a profile). **No canary has run.** These five rows stay unchecked until a human fills the evidence section of `CANARY.md`.

- **Live-DOM verification:** the helper runs the same worker against provider pages on its own host with its own login done by a human out of band. Run `provider-probe.mjs` against the helper's CDP endpoint over the WireGuard path before binding a profile, never while a login is in progress; the canary target is `mock`, so no provider account is touched in the first drill. `UBAG_HELPER_VOICE` additionally depends on `docs/perf-fleet/voice-activation-probe.md` (human-supervised) and is not graduable until that evidence exists.
- **Canary criteria:** the pass conditions of `CANARY.md` steps 1 and drills A and B (job completed on the helper, heartbeat flat, fenced rejects and policy violations unchanged, no duplicate prompt). The manager unreachable case must produce no new grants.
- **Rollback:** the containment order in `RUNBOOK.md`: drain the node, unset `UBAG_HELPER_DISPATCH` (after the drain), then `UBAG_HELPER_PLANE`, then `UBAG_HELPER_NODES`; unset `UBAG_FLEET_MANAGER_URL` to stop polling. Local execution never depended on these flags. Do not delete node tables.
- **Ledger:** one entry per rung, with the node id, the grant generation and the certificate expiry noted (ids and times only, never certificates or keys).

## 5. The two flags that are already live

These are on in production today (D2); only the kill-switch is documented, and nothing here changes them.

- `UBAG_ADMISSION_SHARED`: default on (shared admission across the voice and job lanes). Kill-switch: set `false`, `0`, `no` or `off` and recreate; admission falls back to process-local counters and per-job execution leases are disabled (the memory queue lease becomes the only duplicate guard, P0.2 shard). Not a graduation candidate; do not touch it without a reason from an incident.
- `UBAG_VOICE_LANE_EXCLUSION`: on whenever voice sessions are configured; changes nothing while no voice session holds a browser. Kill-switch: `0`, `false`, `no` or `off` and recreate.

## 6. Ledger entry template

Append to the live ledger in `PROGRESS.md` (keep it to these fields):

```
### Flag graduation: <FLAG>=<value> (<date>)
- Production sha:  <sha>      Stage reached: C0 | C1 | C2
- Live-DOM evidence: <capture file name, or "n/a (gateway-only)">
- Canary evidence:   <where the real output lives; never pasted secrets>
- Soak:              <start, end, stop conditions seen: none | list>
- Rollback rehearsed: <yes/no, how>
- Decided and applied by: <owner>
```

## 7. What this document does not do

- It flips nothing, and it does not add compose passthrough lines (section 1 is a separate reviewed change per flag).
- The canary criteria are proposals. No run was made to calibrate them; the capacity report marks every goal not measured.
- It cannot confirm that a provider's live page still matches the selectors; only the human-run probes can, on the day.
- The check script verifies that names, defaults and passthrough in the docs agree with the code and compose file; it does not prove any behaviour.
