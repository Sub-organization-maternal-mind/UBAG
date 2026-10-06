# Measured capacity report (perf + shared-fleet program, slice P7.9)

NON-AUTHORITATIVE: no isolated lab host exists (D5). **No capacity number is published.** Highest N meeting every goal, per host class: **not measured**.

This is the closing statement for the program's acceptance work. It says what the tooling can measure, what was measured, and what was not,
so nobody quotes a capacity figure that does not exist. The template to fill when a run happens is `capacity-template.md`.

## 1. Identity of this report

| Field | Value |
| --- | --- |
| Integration tip when written | `11e4b8a9c06d7fad7b1da0f33eec54d5cd2c513e` (`feat/perf-fleet`) |
| Harness | `tests/load/ladder.mjs` (P7.2), `tests/load/acceptance.mjs` |
| Mixed manifest `tests/load/workloads/mixed.json` sha256 | `fce07fbf3734eb70fb2662e211ae156b13b237cd5c7aa828aaecfe463adb85f4` (file bytes; a run's `meta.provenance.workload.sha256` is the figure to cite) |
| Host classes and limits to verify | `2c4g` -> 1.5 CPU / 2560 MiB; `4c8g` -> 3 CPU / 5120 MiB (summed container limits, checked from cgroups) |
| Flags on for a published run | copied from `meta.provenance.stack_env`; none were on for any run here because no authoritative run was made |
| Isolated Linux lab host run | **not made** |

## 2. Goal table

Limits are in `tests/load/thresholds.ladder.json`, `thresholds.goals.json` and `thresholds.json`. Under `--require-goals` an unmeasured goal fails.

| Goal | Limit | Result | Status |
| --- | --- | --- | --- |
| Read p95 | <= 100 ms | no authoritative run | not measured |
| Accept p95 (small JSON text create) | <= 200 ms | no authoritative run | not measured |
| Voice relay frame age p95 | <= 100 ms | in-process relay bench only; baseline needs real libopus on Linux (P7.5) | not measured |
| Memory headroom | >= 20 % | no authoritative run | not measured |
| OOM kills | 0 | no authoritative run | not measured |
| Overload (429/503) rate | 0 (harness choice, not a plan number) | no authoritative run | not measured |
| Lost acknowledged / unfinished jobs | 0 | offline harness tests only | not measured |
| Duplicate committed results | 0 | offline harness tests only | not measured |
| Cross-tenant results | 0 | offline harness tests, plus real-helper isolation tests (P7.3, loopback, not containers) | not measured at gateway scale |
| False-success truncation | 0 | offline harness tests only | not measured |
| 100 clients / 1000 queued, consumer paused | accept p95 <= 2000 ms burst bound, read p95 <= 100 ms | no authoritative run | not measured |

## 3. What exists and what ran

Measured (by tests, not capacity): harness logic against a fake gateway (P7.2, 38 ladder cases); a short local smoke against a real gateway
(Windows laptop, SQLite edge store, mock worker), NON-AUTHORITATIVE, which found and fixed the `first_token` payload-policy bug; helper isolation
over real mTLS at 1/2/5/10/20 with zero cross-tenant results on Windows (P7.3, container run not made); relay A/B harness proven offline (P7.7).

Not measured:
- Any N-client ladder on an isolated Linux stack at either host class.
- Live providers (identity-bound capacity: one active operation per provider identity, D6; account inventory unknown, so the ceiling is unverified).
- Live voice media (relay secret unset in production, D7; helper-inclusive voice not driven).
- Helper-inclusive capacity: P4.20 (live canary) is external-blocked and did not run.
- Rust relay: verdict UNEVALUATED (`docs/handoffs/rust-relay-gate-verdict.md`); P7.8 not started.

## 4. How to produce the number

Follow `docs/load-testing.md` ("Workload ladder and capacity report") on an isolated Linux host with compose limits matching the class, with
`--isolated-lab-host --host-class <class> --require-goals`, once with the perf flags off and once on. Copy `capacity-template.md` next to the
run's `report.json`, fill it, and replace sections 1 to 3 here with a link. Never aim the harness at the shared VPS.
