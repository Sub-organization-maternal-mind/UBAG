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
VPS. 1/2/5/10/20-workload step runs are a separate slice (P7.2).

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

## Offline self-tests

```
pnpm test:load:offline
```

Runs `tests/load/acceptance.test.mjs` and `tests/load/workloads.test.mjs` only
(manifest schema check, synthetic fixtures): aggregation math, Retry-After
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
