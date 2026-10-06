# Capacity report template (workload ladder)

Copy this file to `docs/benchmarks/<YYYY-MM-DD>-ladder-<host-class>-<flags>/NOTES.md` next to the run's `report.json` and
`summary.md` (see `README.md` for what to publish and how to review it) and fill in every field. Leave nothing blank: write
`not measured` and say why. The harness, `tests/load/ladder.mjs`, prints most of these values in `summary.md`; this page is the
human-reviewed statement that goes with them. Do not quote a number from a report whose first line says NON-AUTHORITATIVE as capacity.

## 1. Header

First line of the published file, exactly one of:

- `NON-AUTHORITATIVE: <why: laptop | Docker Desktop | host class not verified | no isolated-host attestation>`
- `Lab host: <host name>; isolated Linux host, summed container limits verified from cgroups`

## 2. What was run

| Field | Value |
| --- | --- |
| Date (UTC) | |
| UBAG gateway commit | `meta.provenance.gateway.commit` |
| Harness commit (and `dirty`?) | `meta.provenance.harness` |
| Workload and manifest sha256 | `mixed` / `meta.provenance.workload.sha256` |
| Mode | `ladder` or `paused` |
| Target | `synthetic_chat` (local fixture) / `mock` / other (live providers need a throwaway operator-owned session) |
| Steps, step seconds, min samples, seed | |
| Host class declared / verified | `2c4g` or `4c8g` / `meta.host_class.verified` and `limits_total` |
| Host (CPU model, cores, RAM, kernel, cgroup version) | |
| Container limits | `meta.provenance.container_limits` |
| Isolated-host attestation (`--isolated-lab-host`) | yes / no, who attested |
| `--require-goals` | yes / no |

### Flags that were on

Copy `meta.provenance.stack_env` per container. List explicitly, even when off: `UBAG_WORKER_DAEMON`, `UBAG_WORKER_POOL_SIZE`,
`UBAG_WORKER_STRICT_STREAM_END`, `UBAG_WORKER_STRICT_SUBMIT`, `UBAG_WORKER_STREAM_EVENTS`, `UBAG_WORKER_STREAM_INGEST`,
`UBAG_WORKER_STAGE_TIMINGS`, `UBAG_SYNTHETIC_PROVIDER`, `UBAG_EVENT_NOTIFY`, `UBAG_GATEWAY_STORE`, `UBAG_EXECUTOR_MODE`. A
number is only comparable to another with the same set. Run the ladder with the flags off and with them on and publish both.

## 3. Ladder result

| N | accepted | done | jobs/min | accept p95 ms (goal 200) | read p95 ms (goal 100) | overload % (goal 0) | min mem headroom % (goal 20) | max CPU throttle % | OOM kills (goal 0) | voice p95 ms (goal 100) | verdict |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | | | | | | | | | | | |
| 2 | | | | | | | | | | | |
| 5 | | | | | | | | | | | |
| 10 | | | | | | | | | | | |
| 20 | | | | | | | | | | | |

**Highest N meeting every goal:** `<N or none>`. Goals not measured at that step: `<list or none>` (if any, say UNVERIFIED).
First failed step and the failing goals: `<N: goal names>`.

### Integrity counters (every cell must be 0 for a pass)

| N | lost acked | unfinished | failed | result mismatch | duplicate committed | false-success truncation | cross-tenant leaks | attachment violations |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | | | | | | | | |

### Stage times (needs `UBAG_WORKER_STAGE_TIMINGS=1`)

| N | stage | adapter family | jobs | p50 ms | p95 ms |
| --- | --- | --- | --- | --- | --- |

Bucket resolution, not exact quantiles. If the table is empty say so and why.

## 4. Paused queue (100 clients, 1000 queued, consumer paused)

| Field | Value |
| --- | --- |
| Clients / jobs requested / accepted | |
| Fill seconds | |
| Accept p95 ms (burst limit 2000) | |
| Read p95 ms over the deep queue (goal 100) | |
| Queued at sweep / gateway live queue depth delta | |
| Jobs that left the queue while paused (must be 0) | |
| Drain after resume: completed / failed / lost / unfinished | `not run` if `--resume-wait-seconds` was 0 |
| Store (memory or Postgres) and whether the gateway was restarted between fill and drain | |

## 5. Goal table

One row per goal of `report.json` -> `steps[].goals` (or `paused.goals`), each marked **measured** (passed), **failed** or
**not measured**. Under `--require-goals` a not-measured goal fails the run.

| Goal | Limit | Result at highest passing N | Status |
| --- | --- | --- | --- |

## 6. Caveats (keep the ones that apply, add others)

- Live voice media was not driven: voice frame age is the in-process relay bench (`--voice-latency`), taken on `<host>`; the bench
  size at or above N bounds the step. Not an end-to-end or media-quality figure.
- The fixture (`synthetic_chat`) is a local page: no provider latency, no provider rate limits, no captcha, no login.
- `N` is the number of closed-loop virtual clients with manifest think time, not N real browser sessions; capacity is
  identity-bound (one active operation per provider identity), so this is a ceiling for the tested identity set.
- Per-step memory headroom uses the cgroup `memory.peak`, which accumulates over the container's life (later steps inherit
  earlier peaks).
- `mock` bypasses the warm daemon and any browser; it says nothing about their cost.
- Anything else that makes the number less general (noisy neighbours, a single run, a different kernel).

## 7. Review checklist before committing

- [ ] `report.json` contains no secret, token, key or personal data (the harness copies only allowlisted env names; read it anyway).
- [ ] The first line states NON-AUTHORITATIVE or names the lab host.
- [ ] Flags that were on are listed, including the ones that were off.
- [ ] Every goal is marked measured, failed or not measured.
- [ ] The harness was never aimed at the shared VPS.
