# Acceptance load testing (100 clients, 1,000 queued jobs)

`tests/load/acceptance.mjs` is the acceptance harness for "100 concurrent API
clients and 1,000 queued jobs". It is a zero-dependency Node script (global
`fetch`, async pools). It sits next to the older k6/Locust smoke scripts and does
not replace them.

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
     --scenario all --docker-stats-container ubag-gateway
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
| `overload` | Upload-memory burst (`--burst` x `--burst-body-bytes` facade bodies), an in-flight burst (`--inflight-burst` cheap authenticated GETs), then recovery: health/ready 200 and `--recovery-requests` normal jobs complete. Every 429/503 must carry `Retry-After` and body `retry_after_ms`. |
| `metrics-snapshot` | `/v1/metrics` is scraped before and after every run (and sampled every `--metrics-interval-ms` for gauge maxima); this scenario adds an optional idle window (`--snapshot-seconds`). `--docker-stats-container <name>` samples `docker stats --no-stream` every `--docker-interval-ms` (default 5 s) and is skipped with a note when docker is missing. |

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
- **Resource usage**: docker CPU % and RSS if sampled.
- **Violation samples**: the first few concrete failures.

Default acceptance (`tests/load/thresholds.json`): no 5xx other than 503 overload
with Retry-After; every 429/503 has `Retry-After` and `retry_after_ms`; no hangs;
no lost, unfinished, unaccepted or failed jobs; exactly one job per duplicate
key; malformed/oversized payloads fail with 4xx; cancel races end consistent;
recovery after overload; create p95 under 2000 ms (provider time excluded, the
mock target does not hit a provider).

## Offline self-tests

```
pnpm test:load:offline
```

Runs `tests/load/acceptance.test.mjs` only: aggregation math, Retry-After
backoff, host allowlist, thresholds, and a smoke run of every scenario against
an in-process fake gateway on `127.0.0.1`. It never touches a real host and is
deliberately not part of `pnpm check` or `pnpm test:v0:local`.
