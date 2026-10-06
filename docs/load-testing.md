# Acceptance load testing (100 clients, 1,000 queued jobs)

`tests/load/acceptance.mjs` is the acceptance harness for "100 concurrent API
clients and 1,000 queued jobs". It is a zero-dependency Node script (global
`fetch`, async pools). It supersedes the former k6/Locust/`run-load.mjs` scripts and baselines, which
were deleted because they could report success without proving anything.

**It generates real load. Never point it at the shared production VPS** until you
have measured headroom on an isolated stack (below).

## Safety guards

The harness refuses to run unless all of these hold:

- `--i-understand-this-is-load` is passed (checked first, before anything else).
- `UBAG_LOAD_BASE_URL` and `UBAG_LOAD_API_KEY` are set. The key is sent only as a
  bearer header and is never written to the report or logs.
- The host is loopback (`localhost`, `127.0.0.0/8`, `::1`), RFC1918 private
  (`10/8`, `172.16/12`, `192.168/16`), `fc00::/7`, `fe80::/10`, **or** it is listed
  in `UBAG_LOAD_ALLOWED_HOSTS` (comma separated `host` or `host:port`; wildcards
  are ignored). A docker-network service name such as `gateway` is not an IP, so
  it needs the allowlist.
- Redirects are rejected so the key can't be bounced to another host.

## Run it safely

1. **Isolated stack first.** Start a throwaway gateway with the mock target and
   its own store, for example `make gateway-run` or the small compose profile on
   a dev machine. Use a dedicated app secret and, if you want to see overload
   behavior cheaply, lower the limits on that stack (`UBAG_FACADE_MAX_BODY_BYTES`,
   in-flight and upload-memory settings).
2. **Bounded canary.** Before the full run, run a canary:
   `--scenario queue-1000 --jobs 50 --rate 5`, then `--scenario clients-100
   --clients 10 --iterations 1`. Watch CPU/RSS and `/v1/metrics`.
3. **Full run on the isolated stack.**
   ```
   UBAG_LOAD_BASE_URL=http://127.0.0.1:8080 UBAG_LOAD_API_KEY=... \
     node tests/load/acceptance.mjs --i-understand-this-is-load \
     --scenario all \n     --cgroup-containers gateway=ubag-gateway,browser=ubag-browser,worker=ubag-worker
   ```
4. **Only then consider a shared host**, and only with the numbers from step 3 in
   hand: the production VPS shares CPU and memory with other services, so run the
   canary sizes first, add the host to `UBAG_LOAD_ALLOWED_HOSTS` deliberately,
   and keep `--rate` and `--clients` well under the measured headroom. Never run
   `overload` against a shared host; it exists to exceed limits.

## Scenarios

Select with `--scenario a,b` or `--scenario all` (repeat or comma separate).

| Scenario | What it does |
| --- | --- |
| `queue-1000` | Enqueues `--jobs` (1000) target `mock` jobs with distinct idempotency keys at `--rate` jobs/s, then polls to terminal or `--deadline-ms`. Client backs off per `Retry-After` on 429/503 up to `--max-retries`. |
| `clients-100` | `--clients` (100) concurrent clients, `--iterations` each, rotating: text job, small-image facade call, malformed JSON / malformed data URL, oversized body (must be 413), and with `--with-upload` an artifact PUT. Malformed/oversized must be 4xx; no 5xx, no hangs. |
| `duplicates` | `--dup-keys` keys each fired by `--dup-concurrency` concurrent identical requests: exactly one job id per key. Then `--cancel-races` jobs cancelled immediately (same-key pair plus distinct key) and checked to reach a terminal state that does not flip. |
| `steady-state` | Seeds `--steady-seed-jobs` jobs, then for `--steady-seconds` holds a constant `--steady-rate` creates/s alongside a dedicated GET mix at `--steady-read-rate` reads/s (job, events, list). Latencies land in `steady-state/create` and `steady-state/read`; the goal thresholds gate their p95. Not a burst: keep rates modest. |
| `events-latency` | `--events-subscribers` (20) jobs, each followed by one SSE subscriber on `GET /v1/sse/jobs/{id}` from creation to its terminal event. Delivery latency (`events-latency/event_delivery`) = receive time minus the event's `created_at`, both on the server clock (see below). With `--events-idle-seconds N` the same number of subscribers then idles on the finished jobs for N s while `/v1/metrics` and (optionally) `pg_stat_statements` are diffed over that window. |
| `overload` | Upload-memory burst (`--burst` x `--burst-body-bytes` facade bodies), an in-flight burst (`--inflight-burst` cheap authenticated GETs), then recovery: health/ready 200 and `--recovery-requests` normal jobs complete. Every 429/503 must carry `Retry-After` and body `retry_after_ms`. |
| `metrics-snapshot` | `/v1/metrics` is scraped before and after every run (and sampled every `--metrics-interval-ms` for gauge maxima); this scenario adds an optional idle window (`--snapshot-seconds`). `--cgroup-containers role=container,...` (e.g. `gateway=..,browser=..,worker=..`; legacy `--docker-stats-container <name>` is a one-container alias) reads cgroup files with a read-only `docker exec` every `--docker-interval-ms` (default 5 s), plus one closing sample; a container that cannot be read is skipped with a note (see Resource sampling below). |

Opt-in scenario (never part of `all`):

| Scenario | What it does |
| --- | --- |
| `audio-upload` | Workload `tests/load/workloads/audio-upload.json` (schema: `workload.schema.json`, checked by `tests/load/workloads.mjs`). Sends `--audio-jobs` (5) native multipart `POST /v1/jobs` carrying a deterministic synthetic WAV (`--audio-profile` short 5 s / medium 120 s / large 780 s, default short), re-lists each job's artifacts and requires the stored size and sha256 to match, then settles the jobs like any other. It needs a `--target` whose adapter manifest accepts audio attachments (chatgpt_web, gemini_web, mistral_lechat); `mock` has no attachments policy, so the gateway answers 400 and the run FAILS (`max_audio_upload_violations`). Real providers need an operator-logged-in session: never aim this at the shared VPS. See `docs/perf-fleet/slices/P0.15.md` for the traced path and its gaps. |

Notes:

- The default job command is `chat.prompt` (the mock adapter's supported
  command types are `mock.complete`, `chat.prompt`, `chat.stream`); override with
  `--command-type`. `--target` defaults to `mock`.
- `--max-body-bytes` must match the gateway's JSON body limit (default 1 MiB) so
  the oversized probe really exceeds it.
- The poll sweep is `--poll-interval-ms` (default 1000) with `--poll-concurrency`
  (25) in flight, so a 1000-job drain costs on the order of 1000 requests/s at
  worst. Raise the interval if you are tripping rate limits.

## Reading the report

Output goes to `tests/load/results/<timestamp>/` (git-ignored): `report.json`
(everything) and `summary.md` (what the harness prints). The exit code is 0 on
pass, 1 when `thresholds.json` is violated, 2 when refused or the harness failed.

Sections of `summary.md`:

- **Thresholds**: each limit, the measured value, ok/VIOLATED. Rules are
  `max_<key>` / `min_<key>` against `report.summary`; scenarios not run are skipped.
- **Latency by operation**: per `scenario/op`. `ok` columns are 2xx/3xx attempts
  only; `all p95` includes rejections, which are fast and flatter the number, so
  judge create latency by `ok p95` (`create`, excludes backoff sleeps) and
  `create_incl_retries` for what a caller actually felt.
  `completion` is accept to terminal as seen by polling; `queue_wait_observed` is
  accept to first non-queued poll (granularity is the poll interval; the
  gateway's `ubag_queue_job_wait_duration_seconds` histogram below is exact).
- **Error classes**: `ok`, `client_4xx`, `overload` (429/503 with Retry-After),
  `server_error` (any other 5xx, including a 503 with no Retry-After), `hang`
  (client timeout), `network_error`, `safe_reset` (connection closed on an
  oversized body, treated as safe).
- **Rejections by reason**: client-observed (from the error code) next to the
  gateway's `ubag_admission_rejections_total{reason}` delta.
- **Retry-After handling**: retries, how many honored the full server hint, how
  many were truncated by `--retry-cap-ms`, how many gave up.
- **Gateway metrics**: deltas, plus latency and queue-wait quantiles
  (linear-interpolated from histogram buckets), `max sampled` for gauges
  (in-flight requests, upload memory bytes, admission tokens, DB pool).
- **Resource usage**: per container cgroup CPU throttle %, memory peak and headroom %, `oom_kill`, PSI, and the helper pressure booleans, if sampled.
- **Violation samples**: the first few concrete failures.

Default acceptance (`tests/load/thresholds.json`): no 5xx other than 503 overload
with Retry-After; every 429/503 has `Retry-After` and `retry_after_ms`; no hangs;
no lost, unfinished, unaccepted or failed jobs; exactly one job per duplicate
key; malformed/oversized payloads fail with 4xx; cancel races end consistent;
recovery after overload; create p95 under 2000 ms (provider time excluded, the
mock target does not hit a provider). That 2000 ms is the **100-client burst**
limit; steady-state latency has its own goals (below).

### Event-delivery latency and idle load (`events-latency`)

- **Clock calibration.** The harness and the gateway clocks differ, and the `Date` header only has 1 s resolution, so the
  offset is estimated by sampling `GET /v1/health` (up to `--events-calibrate-ms`, default 2500) and intersecting the
  `[date, date+1 s)` intervals; a sample pair straddling a second boundary pins the offset to a few ms. The report prints
  `offset +/- uncertainty`; judge latencies against that uncertainty (it ignores half the request RTT, fine on loopback/LAN).
  `--events-calibrate-ms 0` takes one sample (+/-500 ms), good for smoke runs only.
- **Live vs backlog.** The stream replays existing events on connect. Only events created after the subscription opened are
  latency samples; replays are counted (`backlog_events`) and ignored. A run that measured zero live events FAILS
  (`min_event_latency_samples`), as do a non-200 or reset stream, a stream with no terminal event within
  `--events-timeout-ms` (`sse_stream_failures`), and out-of-order sequence numbers, duplicate event ids or another job's
  events (`sse_event_violations`). `event_latency_p95_ms` is reported but has no threshold until the lab host sets one.
- **Idle window** (`--events-idle-seconds`): the report shows the heartbeats received, the `ubag_gateway_http_requests_total`
  delta by route, and the DB-pool / `ubag_sse_connections_current` series over just that window.
- **Optional `pg_stat_statements` delta**: `--pg-stat-container <name>` (plus `--pg-user`/`--pg-db`, default `ubag`) runs a
  read-only `docker exec ... psql` snapshot before/after (the stats are never reset) and reports total calls, total exec ms and
  the top 10 statements by calls (`events_idle_pg_calls_per_s` is derived when an idle window ran). The extension must be
  enabled on the test database; if it is not (or docker is missing) the section says `skipped` and the run is unaffected.
- `/v1/events` is a paged JSON list in the gateway (not a stream), so only the per-job stream is probed.
- Run this against an isolated stack only; numbers from a laptop/Docker stack are NON-AUTHORITATIVE.

The gateway-metrics section now also lists `ubag_gateway_http_requests_total` deltas per route, `ubag_sse_connections_current`,
`ubag_worker_*`, and every job/worker/stage histogram (`ubag_worker_job_duration_seconds`, `ubag_job_stage_duration_seconds`,
`ubag_jobs_duration_seconds`, ...) with quantiles.

### Integrity gates (fail closed)

Every job that reaches `completed` is re-fetched and verified, so a green run
means the results were right, not just that the statuses were terminal:

- **Result body**: `GET /v1/jobs/{id}` must return the same `job_id`, status
  `completed`, and non-empty `result.output.text`. For the `mock` target the text
  must also name the job, end with the submitted prompt (the idempotency key is
  appended to every prompt as a nonce) and contain the nonce exactly once, so a
  wrong, truncated or duplicated result is `result_mismatches`.
- **Event log**: `GET /v1/jobs/{id}/events` (paged, bounded) must contain exactly
  one terminal event and no duplicate sequence numbers (`terminal_event_violations`).
- **Warnings**: `completed_with_warnings` is **not** a success. It is counted in
  `warning_jobs` (threshold 0), separately from `completed` and `failed`.
- **Second tenant**: set `UBAG_LOAD_API_KEY_B` to a key of a *different tenant*
  (it must differ from `UBAG_LOAD_API_KEY`; never written to reports). Up to
  `--tenant-probe-samples` (10) completed jobs are probed as tenant B on GET job,
  events, artifacts and the job list: anything but 403/404 on a direct read, or a
  tenant-A job id in tenant B's list, is `cross_tenant_leaks`; a 401 or other
  surprise is `tenant_probe_unexpected` (so a bad key can't pass silently).
- **facade_image**: the OpenAI-facade image request must return 200 with message
  content; any other final outcome is `facade_image_failures` (threshold 0).
- **Overload**: when `overload` runs it must produce at least
  `min_overload_rejections` (1) 429/503 responses; zero means the limits were never
  reached and is a FAIL, not a note.

### Goal thresholds and `--require-goals`

`tests/load/thresholds.goals.json` holds the perf goals: steady-state create
p95 <= 200 ms, steady-state read p95 <= 100 ms (the dedicated GET mix of the
`steady-state` scenario), plus minimum verified results/events/tenant probes.
Pass `--require-goals` to merge it over `thresholds.json` **and** treat every
unmeasured threshold as a FAIL. Use it with `--scenario all` and
`UBAG_LOAD_API_KEY_B` set and `--voice-latency <report>` (see "Voice relay
latency" below); omitting a scenario, the second tenant or the voice report fails
the run instead of passing silently. Numbers measured on a laptop/Docker stack are
NON-AUTHORITATIVE (there is no isolated lab host yet); never aim this at the shared
VPS. The 1/2/5/10/20-workload closed-loop ladder, the 100-client / 1000-queued paused mode and the capacity report are
`tests/load/ladder.mjs` (next section).

### Workload ladder and capacity report (`tests/load/ladder.mjs`)

`ladder.mjs` answers "how many concurrent workloads can this stack carry while every goal still holds". It reuses the
acceptance harness (HTTP client, Retry-After backoff, polling, result/event/tenant verification) and adds the closed-loop
ladder, a consumer-paused queue mode and the capacity report. It is **not** part of `pnpm check`; same safety guards as
above (acknowledgement flag, loopback/private host or an explicit `UBAG_LOAD_ALLOWED_HOSTS`, never the shared VPS).

```
UBAG_LOAD_BASE_URL=http://127.0.0.1:8080 UBAG_LOAD_API_KEY=... UBAG_LOAD_API_KEY_B=... \
  node tests/load/ladder.mjs --i-understand-this-is-load --workload mixed --step-seconds 120 \
  --cgroup-containers gateway=<c>,browser=<c>,worker=<c> --host-class 2c4g --isolated-lab-host \
  --voice-latency voice-latency.json --require-goals
```

**Ladder mode (default).** For each step N of the workload manifest's `ladder_steps` (1, 2, 5, 10, 20; override with
`--steps`), N virtual clients run for `--step-seconds`. A client is closed-loop: pick a class from the manifest `mix`
(deterministic per `--seed`), create the job, poll it to a terminal state, verify the result body, the event log and the
tenant boundary (second key), sleep the manifest think time (`--think-scale` percent), repeat. At most one job per client
is in flight. A step runs for at least `--step-seconds` and, up to 3x that, until `--min-samples` (20) text accepts and
polls were timed, so a slow step still yields a p95 (fewer samples leave the latency goal unmeasured, never a lucky number).

| Class | What is sent |
| --- | --- |
| `text` | JSON `POST /v1/jobs`, prompt padded to a size drawn from the class' `payload_bytes` |
| `attachment` | native multipart job with one `text/plain` document (declared; size and sha256 verified from the artifact list). On the `mock` target, which declares no attachments policy, the artifact `PUT` path of `--with-upload` is used instead and a 4xx is tolerated |
| `audio_upload` | native multipart job with the synthetic WAV profile closest to the drawn size (needs a target that accepts `audio/wav`; `synthetic_chat` does, `mock` does not and the step FAILS on `attachment_violations`) |
| `voice_session` | not driven: live media needs a relay secret and a real browser (D7). Workload `voice` is refused |

`mixed` targets `synthetic_chat` (P7.1) so jobs reach the warm-daemon path; `text` and `attachment` target `mock`.
A live provider is refused unless `--target` names it **and** `--allow-live-provider` is passed (user-owned session; never
at ladder load by default).

**Goals per step.** The merged set is the integrity zeros of `thresholds.json`, the plan goals of `thresholds.goals.json` and
the ladder-only keys of `thresholds.ladder.json`. Every goal is marked `passed`, `failed` or `not_measured`; `not_measured`
fails only under `--require-goals`.

| Goal | Limit | Measured as |
| --- | --- | --- |
| accept p95 | 200 ms | ok `POST /v1/jobs` of the text class (all creates if the mix has no text); attachment/audio accept is reported per class, ungated |
| read p95 | 100 ms | ok `GET /v1/jobs/{id}` polls issued by the clients |
| overload rate | 0 % (a harness choice, not a plan number) | 429/503 attempts over create attempts: a step that needed admission refusals did not sustain N |
| memory headroom / OOM kills | >= 20 % / 0 | per-step cgroup sampler (`memory.peak` is lifetime-cumulative, so later steps inherit earlier peaks) |
| voice relay p95 | 100 ms | `--voice-latency` bench row with the smallest session count >= N (a ceiling, labelled as bench, not live media); none above 20 |
| integrity | 0 | lost acked jobs (404), unfinished, failed, result mismatches, cross-tenant leaks, duplicate committed results (duplicate result bodies + duplicate terminal events + one job id for two keys), false-successful truncation (`mock`/`synthetic_chat` echo check; `--truncation-probes N` also sends the fixture's deadline-cut scenario and requires a non-`completed` outcome), attachment violations |

Without `UBAG_LOAD_API_KEY_B` the tenant probe count is 0 and `min_tenant_probe_requests` fails: isolation unverified is a FAIL.
CPU headroom, throttle %, host pressure and the per-stage p50/p95 (`ubag_job_stage_duration_seconds` delta between the step's two
scrapes; needs `UBAG_WORKER_STAGE_TIMINGS=1` and a target that reaches a worker) are reported, not gated.

**Capacity.** "Highest N meeting every goal" is the longest passing prefix of the executed steps. The ladder stops at the first
failing step (`--continue-after-fail` runs the rest; the capacity is still that prefix). Capacity is identity-bound (one active
operation per provider identity, D6), so the figure is a ceiling for the tested identity set, not a fleet number. If any
goal was not measured at that step the report says UNVERIFIED.

**Host class and authority.** `--host-class 2c4g|4c8g` is checked against the *sum* of the sampled containers' CPU and memory
limits (`--cgroup-containers`): 2c/4G is 1.5 CPU / 2.5 GiB, 4c/8G is 3 CPU / 5 GiB, within 5 %; an unlimited or unreadable
container fails the check. A report is authoritative only with `--isolated-lab-host` (the operator's attestation that no other
workload shares the box), a verified host class and cgroup files actually read; otherwise its first line says
`NON-AUTHORITATIVE` and why. `meta.provenance.stack_env` lists the perf-fleet flags that were on (`UBAG_WORKER_DAEMON`,
`UBAG_WORKER_POOL_*`, `UBAG_WORKER_STRICT_*`, `UBAG_WORKER_STREAM_*`, `UBAG_WORKER_STAGE_TIMINGS`, `UBAG_SYNTHETIC_PROVIDER`,
`UBAG_WORKER_CONSUMER_ENABLED`, ...). Run the ladder with the flags off and on, and publish both.

**Paused mode (`--mode paused`).** Start the stack with the worker consumer disabled (`UBAG_WORKER_CONSUMER_ENABLED=false`; it is
embedded in the gateway, so there is no pause API). `--clients` (100) clients enqueue `--jobs` (1000) jobs; reads (job, events,
list) are timed for `--hold-seconds` at `--read-rate`/s against the deep queue; every acked job is then swept once and must still
exist and still be queued. Gated: accept p95 under the 100-client **burst** limit (`max_create_p95_ms`, 2000 ms), read p95 100 ms,
`min_queued_jobs`, `min_queue_depth_observed` (`ubag_queue_depth_live` delta), zero `consumer_progressed`. With
`--resume-wait-seconds N` the harness then prints a prompt, waits up to N s (quietly: a restart is expected) for the first job to
leave the queue, and drains and verifies every acked job (lost, unfinished, result, events, tenant probe). Without it the
execution-dependent goals are not measured (a FAIL under `--require-goals`). Use the Postgres store if you restart the gateway
in between: with the in-memory store the jobs are gone and are reported as lost.

**Exit code.** 0 when every step passed, 1 when a goal failed, 2 when refused or the harness failed. Output: `report.json` and
`summary.md` under `tests/load/results/<timestamp>-ladder|paused/`. To publish, follow `docs/benchmarks/README.md` and fill in
`docs/benchmarks/capacity-template.md`.

What it cannot show: real-provider latency (the fixture is a local page), live voice media, N real browser sessions beyond what
the stack runs, or anything about the shared VPS. The latency goals judge the API (accept, read): read `jobs/min` and
completion p95 next to them, because with a single consumer the throughput flattens at a small N while accept p95 still
passes, and then the executor, not the API, is the limit. Numbers from a laptop or Docker Desktop stack are NON-AUTHORITATIVE.

### Voice relay latency (`--voice-latency`)

The gateway-side voice relay path has two measurement points. Both are taken
inside the gateway process on its own monotonic clock, so they bound gateway
queueing and scheduling only: not the client network, TURN, WebRTC jitter
buffers, or the browser audio stack.

| Direction | Start stamp | End stamp |
| --- | --- | --- |
| `mic` | the gateway reads the client's RTP packet (`track.ReadRTP` returns; the frame is enqueued on the bounded mic channel) | `relay.Send` returned for that frame (includes mic-queue wait and the relay socket write) |
| `speaker` | `relay.Recv` returned the provider frame | `track.WriteSample` returned for that frame |

Production exposes the same quantity as the histogram
`ubag_voice_relay_frame_age_seconds{direction="mic|speaker"}` (buckets 5 ms to
1.28 s). Speaker frames lost to a failed `WriteSample` are now counted in
`ubag_voice_media_frames_dropped_total{direction="speaker"}` (the series used to be
declared but never incremented).

Client-link quality is sampled from `pc.GetStats()` every 2 s per live media
session into label-free series: `ubag_voice_inbound_jitter_seconds` (histogram),
`ubag_voice_inbound_packets_received_total` / `ubag_voice_inbound_packets_lost_total`
(loss ratio = lost / (received + lost)) and the gauge `ubag_voice_mic_queue_depth`
(frames queued toward relays, summed across sessions at scrape time).

Age bound (`UBAG_VOICE_QUEUE_MAX_AGE_MS`, default `0` = off): the mic channel and
the speaker path are bounded by 64 packets, which is about 1.28 s only for 20 ms
packets (the relay accepts up to 120 ms, so up to about 7.7 s). With a value above 0
a queued frame older than that is dropped instead of forwarded and counted in
`ubag_voice_media_frames_dropped_total{direction="mic_age"}` /
`{direction="speaker_age"}`; the count bound keeps dropping the oldest frame as
before (`mic` / `speaker`). With the bound on, the speaker pump drains the relay
socket into its own bounded queue and a separate writer forwards it. Ages are
stamped inside the gateway; kernel FIFO, pipe and TCP buffers are out of reach
(P7.6). The latency bench below honours the same env var, so on/off can be compared.

Offline bench (no network, no VPS): a real `MediaHub` plus a pion client over
loopback against the in-test `fakeRelay` echo, client mic paced at the 20 ms
Opus frame duration, sessions ramped 1/5/10/20 with one `fakeRelay` each:

```
cd apps/gateway
UBAG_VOICE_LATENCY_REPORT=voice-latency.json go test ./internal/voice -run NONE -bench RelayLatency -benchtime 1x
```

`UBAG_VOICE_LATENCY_SECONDS` (default 3) sets the per-size measurement window.
The bench reports exact p50/p95/p99 per direction as bench metrics and, when
`UBAG_VOICE_LATENCY_REPORT` is set, writes a `ubag-voice-latency/v1` JSON report.
Feed it to the harness with `--voice-latency voice-latency.json`: the run records
`voice_relay_p95_ms` (the worst p95 over all sizes and both directions) and the
goal `max_voice_relay_p95_ms` (100, in `thresholds.goals.json`) applies. Under
`--require-goals` a missing or malformed report leaves the goal unmeasured, which
FAILs. Numbers from this bench are NON-AUTHORITATIVE (laptop loopback; on Windows
the clock granularity quantises sub-millisecond ages). This is the baseline any
future relay rewrite decision needs; it is not a media-quality or end-to-end
latency measurement.

### Relay-side stats (`UBAG_VOICE_RELAY_STATS=1`)

The browser-container audio relay (`deploy/vps/browser/audio-relay.py`) has an
opt-in, off-by-default measurement mode with no protocol change. While a session
is active it writes one `audio-relay: stats {json}` line to
`/run/ubag/audio-relay.log` every 10 s, and a final one when the session ends.
Fields: `mic_frames`, `mic_decode_errors`, `mic_fifo_dropped`,
`mic_muted_discarded`, `speaker_frames`, `speaker_encode_errors`, `mic_fifo_bytes`
(unread bytes in the mic FIFO, `null` when unknown), and for each direction
`<dir>_samples`, `<dir>_p50_ms`, `<dir>_p95_ms`, `<dir>_max_ms` over the window.
Only 1 frame in 10 is timed (at most 512 samples per window). `mic` times decode
plus the FIFO write; `speaker` times Opus encode plus the gateway socket write.
Neither includes time spent waiting for the next frame, so these complement, not
replace, the gateway-side frame age above.

### Relay baseline profile and stop rule (`bench_relay.py`)

`deploy/vps/browser/tests/bench_relay.py` is the Rust-gate control: it profiles
the Python relay with real libopus and evaluates the pre-registered stop rule
before any rewrite work. Run it in the browser image on an isolated container,
never on the shared VPS:

```
docker run --rm --cap-add SYS_PTRACE -v "$PWD":/w -w /w/deploy/vps/browser <image with python3, libopus0, pulseaudio-utils>   python3 tests/bench_relay.py --sessions 1,5,10 --duration 30 --pulse-check --pyspy   --host-class "<where this ran>" --container-cpu-pct <live-call container CPU %> --out relay-baseline.json
```

N sessions are N relay processes (the relay serves one session per audio
environment), each fed by a paced 20 ms real-libopus client and a fake parec.
Reported per relay: CPU per call-minute, peak RSS, thread count, wake lag of a
10 ms sleeper thread, mic FIFO-full drops, p50/p95/max frame turnaround, and the
CPU split into libopus, syscalls and the rest (thread CPU time around the native
calls; `--pyspy` adds `py-spy --native` and `--native --gil` as corroboration,
and ctypes releases the GIL during libopus so `--gil` shows GIL holders, not
codec time). `--pulse-check` runs `pactl list short sinks|sources` and flags a
sink or monitor whose sample spec differs from the relay's 48 kHz mono s16le
(hidden PulseAudio resampling, which costs the container, not the relay).

Stop rule (roadmap-proposed, evaluated first): STOP if at least 70% of relay CPU
is in libopus plus syscalls, or the relay is under 5% of container CPU
(`--container-cpu-pct`, measured separately on a real container during a live
call). The plan's own gate (at least 20% UBAG-controlled overhead, or at least
25% CPU/memory, material in the mixed workload) is printed alongside. Verdicts:
`stop`, `continue`, `inconclusive` (rule B not measurable), `invalid` (fake codec
or no real FIFO). Non-lab numbers are NON-AUTHORITATIVE; fake parec, the client
and real PulseAudio/Chrome CPU are outside the relay's figures. The pure rule and
parser logic is covered by `tests/test_bench_relay_rules.py`.

### Rust-gate A/B harness and evaluator (`tools/voice-relay-bench`, `tools/benchmark/rust-gate.mjs`)

The pre-registered rule for ever replacing the Python relay lives in `tests/load/voice-relay-gate.json` (seed, 95 % CI, limits); the verdict record is
`docs/handoffs/rust-relay-gate-verdict.md`. No Rust exists; both tools are the machinery a candidate would be judged by.

`tools/voice-relay-bench/run.mjs` drives any relay implementation as a black box over the framed TCP protocol on identical seeded input: a byte compare over 66
scenarios (the shared v2 fixture's hello and framing cases, every Opus duration, corrupt, empty and max-size packets, mute interleavings, a partial final PCM chunk,
busy, stale lease generation), interleaved paired runs (CPU per call-minute and RSS from `/proc`, mic and speaker transit p50/p95/p99, drops, optional
`--mixed-probe`) and a reconnect leak check. A parec shim, a pactl shim and a FIFO sink stand in for PulseAudio; the relay under test sees only its existing env seams.

```
node tools/voice-relay-bench/run.mjs --self-test                      # Python vs Python, fake codec: proves the harness (offline, any OS, ~1 min)
node tools/voice-relay-bench/run.mjs --a "python3 deploy/vps/browser/audio-relay.py --addr {addr}" --b "<candidate> --addr {addr}" --lab-host --host-class "<helper>" --out ab.json
node tools/benchmark/rust-gate.mjs --baseline relay-baseline.json --ab ab.json --rollback-drill pass --require go
```

The evaluator answers `stop`, `prototype` (stage 0 only: the P7.5 profile permits writing a candidate), `go`, `inconclusive` or `invalid` (self-test, laptop, fake codec,
another libopus or seed). Claims are judged on the paired CI, not the point estimate. Real runs belong on a dedicated Linux lab helper with real libopus, never the shared
VPS; anything else is NON-AUTHORITATIVE. `pnpm test:rust-gate` runs the offline tests (needs Python 3.9+ for the self-test cases, which skip without it).

## Offline self-tests

```
pnpm test:load:offline
```

Runs `tests/load/acceptance.test.mjs`, `tests/load/ladder.test.mjs`, `tests/load/workloads.test.mjs` and
`tests/load/baseline-matrix.test.mjs` only (manifest schema check, synthetic fixtures, the workload ladder and its capacity
rules): aggregation math, Retry-After
backoff, host allowlist, thresholds, a smoke run of every scenario against
an in-process fake gateway on `127.0.0.1`, and negative cases that must FAIL
(wrong/truncated/duplicated result, duplicated terminal event, cross-tenant leak,
`completed_with_warnings`, facade 400s, zero overload rejections, unmeasured goals). It never touches a real host and is
deliberately not part of `pnpm check` or `pnpm test:v0:local`.

## Resource sampling (cgroup, throttle, host pressure)

`docker stats` is gone. Per sample the harness runs one read-only
`docker exec <container> sh -c 'cat ...'` that reads `cpu.stat`, `cpu.max`,
`memory.current/peak/max/events`, `cpu.pressure`, `memory.pressure`,
`/proc/meminfo` and `/proc/stat` (cgroup v2), falling back to the v1 files
(`cpu/cpu.stat`, `cpuacct.usage`, `memory.usage_in_bytes`, `max_usage_in_bytes`,
`limit_in_bytes`, `oom_control`; PSI is v2 only). The container needs `sh` and
`cat`. Code: `tests/load/lib/cgroup.mjs`.

`report.json` -> `resources` (and `summary`, which thresholds read):

| field | meaning |
| --- | --- |
| `containers.<role>.cpu_throttled_pct` | share of CFS periods in which the cgroup was throttled, over the run (0 without a CPU limit). `summary.cpu_throttled_pct` is the max over containers. |
| `containers.<role>.memory_headroom_pct` | `(1 - peak / memory.max) * 100`, peak = max(`memory.peak`, sampled `memory.current`). `null` when the container has no memory limit. `summary.memory_headroom_pct` is the minimum over containers; goal `>= 20`. |
| `containers.<role>.oom_kills` | `oom_kill` delta over the run; `summary.oom_kills` is the sum, goal `0`. |
| `host_pressure` | booleans for the helper pressure rules: `cpu_over_80`, `mem_available_under_20`, `pressure_triggered`, `recovered` (CPU `< 60%` and MemAvailable `> 25%` for the whole `--pressure-window-ms`, default 120000; `null` if never triggered). Host CPU % comes from `/proc/stat` deltas. |

`--require-goals` fails closed: `min_memory_headroom_pct` / `max_oom_kills` are
unmeasured (FAIL) unless every sampled container has a memory limit. Throttle %
and the pressure booleans are reported, not gated. `/proc/*` is the host's (the
VM's on Docker Desktop) unless the runtime virtualises it. Numbers off the lab host
are NON-AUTHORITATIVE.

## Workload manifests and run provenance

`--workload <name>` (`text`, `attachment`, `audio-upload`, `mixed`, `voice`; files in `tests/load/workloads/`,
schema `tests/load/workloads/workload.schema.json`) validates a manifest before any load is generated and records its
name, SHA-256 and body in `report.json` -> `meta.provenance.workload`. It does not change the traffic: that is still
selected with `--scenario`. Every report also carries `meta.provenance`: gateway commit (from `ubag_gateway_info`),
harness git sha (+ `dirty`), harness host spec, allowlisted env knobs (harness process and, with `--cgroup-containers`,
each container via read-only `docker exec <c> env`) and container CPU/memory limits. Only allowlisted names are ever
copied (`tests/load/lib/provenance.mjs`). Publishing rules: `docs/benchmarks/README.md`.
