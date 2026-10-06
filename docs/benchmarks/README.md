# Benchmarks

Published load-test results live here. `tests/load/results/` is git-ignored: raw run output stays local, and only a
reviewed run is copied into this directory.

## What to publish

For one run copy `report.json` and `summary.md` from the run directory into
`docs/benchmarks/<YYYY-MM-DD>-<workload>-<short-label>/`, and add a short `NOTES.md` stating:

- which flags were on (the `provenance.stack_env` block lists the allowlisted env of each sampled container),
- whether the numbers are **authoritative**. Only a run on the isolated lab host is. Anything from a laptop, Docker Desktop
  or the shared VPS is **NON-AUTHORITATIVE** and must say so in its first line. Never aim the harness at the shared VPS.

Before committing a report, check `report.json` for anything you would not put in a public repository. The harness copies
only allowlisted knobs and withholds odd-looking values, but the run directory is yours to review.

## Provenance in every report (`meta.provenance`)

| field | source |
| --- | --- |
| `gateway` | `version`, `api_version`, `commit` from the `ubag_gateway_info` series of `/v1/metrics` (`null` if metrics were unreadable) |
| `harness` | `git rev-parse HEAD` of the checkout running the harness, and `dirty` when `tests/load` has uncommitted changes |
| `harness_host` | CPU count/model, memory, OS of the machine running the harness (not necessarily the gateway host) |
| `harness_env` | allowlisted names from the harness process environment (`UBAG_WORKER_CONCURRENCY`, `UBAG_WORKER_DAEMON`, `UBAG_WORKER_POLL_INTERVAL_MS`, `UBAG_ADMISSION_*`, `UBAG_GATEWAY_MAX_INFLIGHT_REQUESTS`, `UBAG_GATEWAY_STORE`, `UBAG_VOICE_STORE`, `UBAG_EXECUTOR_MODE`, `GOMAXPROCS`, `GOMEMLIMIT`) |
| `stack_env` | the same allowlist read with a read-only `docker exec <container> env` for each `--cgroup-containers` target |
| `container_limits` | CPU cores and memory MB limits of each sampled container (`null` = unlimited) |
| `workload` | name, SHA-256 and full body of the `--workload` manifest, or `null` |

`harness_env` is only meaningful when the operator exported the stack's values before running; `stack_env` is the reliable
source for a Docker stack. Neither is ever read for names outside the allowlist.

## Workload manifests

`tests/load/workloads/{text,attachment,audio-upload,mixed,voice}.json`, validated by `tests/load/workloads.mjs` against `tests/load/workloads/workload.schema.json`
(`pnpm test:load:offline`). `harness_support: implemented` means a harness generates that traffic today (`text`,
`attachment`, `audio-upload`, `mixed`: `tests/load/ladder.mjs` drives them as a closed-loop ladder); `planned` manifests record
the intended workload (`voice` needs live media and can only be run supervised in a lab). The `mixed` weights are a
roadmap proposal, not measured production shares. For `acceptance.mjs`, `--workload <name>` validates the manifest and
records it in the report; the traffic that actually runs is still chosen with `--scenario`. For `ladder.mjs` the manifest
drives the traffic (mix, sizes, think time, steps).

## Publishing a ladder run

A `tests/load/ladder.mjs` run (`report.json`, `summary.md`) is published the same way, under
`docs/benchmarks/<YYYY-MM-DD>-ladder-<host-class>-<flags>/`, with a `NOTES.md` written from
[`capacity-template.md`](capacity-template.md): header (NON-AUTHORITATIVE or the lab host), commit SHAs, host class and whether
its limits were verified, the flags that were on, the ladder table, the highest N meeting every goal, every goal marked
measured, failed or not measured, and the caveats. A run from a laptop or Docker Desktop stack is NON-AUTHORITATIVE and says so in
its first line. Details: `docs/load-testing.md` ("Workload ladder and capacity report").
