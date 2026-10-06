# Rust voice-relay gate: verdict record

Perf-fleet slice P7.7. Source of truth for the rule: `tests/load/voice-relay-gate.json` (pre-registered; do not edit it after seeing numbers).
Evaluator: `tools/benchmark/rust-gate.mjs`. Harness: `tools/voice-relay-bench/run.mjs`.

## Verdict (current)

**UNEVALUATED. No Rust has been written, and none may be written or adopted until this file says otherwise.**

Nothing has been measured on an authoritative host. The P7.5 baseline profile (`deploy/vps/browser/tests/bench_relay.py`) needs real libopus on Linux and a
live call's container CPU; the dev box that built this slice has neither, no Docker and no lab helper. The A/B harness and the evaluator are built and
verified offline (Python relay against itself with a fake codec, negative controls, evaluator unit tests), which says nothing about relay cost.

| field | value |
| --- | --- |
| verdict | unevaluated (the evaluator would answer `invalid`: no baseline report exists) |
| libopus version | not measured |
| host class | not measured (needs the dedicated lab helper, 2c/4G) |
| stage 0 (P7.5 stop rule, overhead ceiling) | not run |
| stage 1 (A/B against a candidate) | not run: no candidate exists (P7.8 is not started) |
| rollback drill | not run: the `UBAG_VOICE_RELAY_IMPL` switch does not exist yet |
| decision | P7.8 (Rust relay crate) stays NOT STARTED; P7.6's optimised Python stays the relay |

Static analysis (P0 reader notes) expects a `stop`: the relay is thin glue around libopus, so a rewrite would FFI the same library, and a Rust relay would be a
third runtime in a project that already pruned SDK languages. That is a hypothesis, not a result.

## The pre-registered rule

A candidate is adopted only if every line holds. Claims are judged on the paired 95 % CI (Student t over interleaved A/B pairs), never on the point estimate;
a CI that straddles its limit is `inconclusive` and asks for more pairs.

| # | requirement | key in the gate file |
| --- | --- | --- |
| 0 | stage 0, from the P7.5 profile: stop if libopus plus syscalls hold >= 70 % of relay CPU (rule A) or the relay is < 5 % of one live call's container CPU (rule B); a prototype is authorised only if the Python-owned overhead (relay CPU outside libopus and syscalls) is >= 20 % of relay CPU. Authorises writing a candidate, never adopting one | `stop_libopus_syscall_share_pct`, `min_relay_share_of_container_pct`, `min_python_overhead_share_pct` |
| 1 | any ONE win: UBAG-controlled overhead (relay CPU minus libopus self-time) down >= 20 %, or CPU per call-minute down >= 25 %, or peak RSS down >= 25 %; CI lower bound | `min_cpu_overhead_reduction_pct`, `min_cpu_reduction_pct`, `min_rss_reduction_pct` |
| 2 | no regression: mic and speaker frame-transit p95 and p99 (CI upper bound <= 0 %), no extra dropped frames | `max_p95_regression_pct`, `max_p99_regression_pct`, `max_extra_drops` |
| 3 | no leak over 100 reconnects (fds, threads, child processes, RSS growth <= 4 MiB) | `max_leaks`, `max_leak_rss_growth_kib`, `leak_reconnects` |
| 4 | byte-identical output on the seeded corpus (control replies, speaker packets, mic FIFO bytes, close behaviour) and the shared HMAC token vectors reproduce; the recorded libopus golden exists for this libopus version | `max_byte_mismatches`, `corpus.seed` |
| 5 | material in the mixed workload: the relay is >= 5 % of a live call's container CPU AND the mixed run (N mock-provider browser workloads plus one voice call, A/B interleaved) improves container CPU, host headroom or job p95 with a CI lower bound above 0 % | `min_relay_share_of_container_pct`, `min_mixed_improvement_pct` |
| 6 | at least 5 interleaved pairs on an authoritative host (isolated Linux lab helper, real libopus) | `min_pairs`, `require_authoritative` |
| 7 | the rollback drill passes: `UBAG_VOICE_RELAY_IMPL` back to `python` (or the candidate binary removed) restores the Python relay with no gateway change | `--rollback-drill pass` |

The baseline for B is the OPTIMISED Python (P7.6), so the candidate does not get credit for fixes Python can also take. libopus must be the same `.so` in A
and B; the operator attests that, and a vendored build that changes encoder bytes fails the byte compare.

## How to produce the verdict (human steps, never on the shared VPS)

1. Baseline profile (P7.5). On the lab helper, in the browser image or `debian:bookworm-slim` with `python3 libopus0 pulseaudio-utils`:
   `python3 tests/bench_relay.py --sessions 1,5,10 --duration 30 --pulse-check --pyspy --lab-host --host-class "<helper>" --container-cpu-pct <X> --out relay-baseline.json`
   where `<X>` is the whole browser container's CPU (percent of one core) during one live voice call.
2. Stage 0: `node tools/benchmark/rust-gate.mjs --baseline relay-baseline.json --require prototype`.
   `stop` closes the experiment (close P7.8 and keep P7.9); `inconclusive` means rule B still needs the live-call container figure; only `prototype` lets P7.8 start.
3. Candidate (P7.8, only after a `prototype` verdict): a relay that honours the harness seams (`UBAG_VOICE_RELAY_SECRET`, `UBAG_VOICE_RELAY_ADDR`,
   `UBAG_VOICE_MIC_PIPE`, `UBAG_VOICE_PAREC`, `UBAG_VOICE_PACTL`, `UBAG_VOICE_RELAY_IDLE_S`) and links the same libopus.
   The P7.8 start checklist (crate, both browser images, selector, CI, rollback drill) is in `docs/perf-fleet/slices/P7.8.md`.
4. A/B on the lab helper (Linux, node >= 20, python3, libopus0; no PulseAudio, no browser, no provider):
   `node tools/voice-relay-bench/run.mjs --a "python3 deploy/vps/browser/audio-relay.py --addr {addr}" --label-a python --b "<candidate> --addr {addr}" --label-b rust --lab-host --host-class "<helper>" --pairs 5 --duration 30 --out ab.json [--mixed-probe "<cmd>"]`
   The mixed probe prints `{container_cpu_pct, job_p95_ms, host_headroom_pct}` for the window of each run (for example the P7.2 ladder `mixed` workload plus `docker stats`).
5. Rollback drill (human): switch `UBAG_VOICE_RELAY_IMPL` between `rust` and `python` on a canary container, confirm a call connects each way, then record `pass` or `fail`.
6. Verdict: `node tools/benchmark/rust-gate.mjs --baseline relay-baseline.json --ab ab.json --rollback-drill pass --out verdict.json --markdown verdict.md --require go`.
   Paste `verdict.md` below with the libopus version, host class, CI bounds and the commit shas of A and B.

## Decision record (fill in; leave the table above as the current state until then)

| date | libopus | host class | A sha | B sha | pairs | verdict | CI bounds (win, regressions, leaks) | decided by |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| (none yet) | | | | | | | | |

## Known limits of the evidence

- The corpus packets are Opus-shaped (valid TOC, seeded random payload), not speech. libopus decodes them deterministically, which is what a byte compare needs, but
  per-frame CPU on real speech can differ. A and B see identical input, so the paired delta is still fair; absolute numbers are not capacity numbers.
- Black-box CPU is the whole relay process, libopus included. The overhead rule subtracts the libopus self-time from the P7.5 in-process profile (thread CPU time
  around the native calls); that figure is measured in an instrumented process, so it is an estimate of the same quantity, not the same measurement.
- The harness reads the mic FIFO by polling every 1 ms and stamps with `process.hrtime`, on the same host as the relay; its own CPU competes with the relay.
  Both sides pay it equally. Transit times are harness-observed, not gateway frame age.
- `run.mjs --self-test` proves the harness only: it moves exactly the same frames (0 % delta on the work counters) and the byte compare detects a broken speaker
  encoder, mic decoder or mute. Resource metrics between two runs of the same relay are noise; run an A/A pair on the lab host to learn the noise floor before trusting a small delta.
- Windows has no `/proc` and no FIFO: CPU, RSS and leak checks are `null` / unsupported there, never invented.
- Not covered by the default corpus: the 5 s hello timeout (`--include-slow`), the 30 s idle timeout against client DTX silence (a protocol question, not a port
  question), a relay started with no secret, PulseAudio device loss.
