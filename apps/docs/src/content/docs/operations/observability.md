---
title: Observability
description: Logs, metrics, traces, profiling, error aggregation, dashboards, and SLOs — all implemented in Phase 6.
---

**Status:** Implemented in Phase 6. See [ADR 0010](/adrs/0010-phase6-wasm-observability).

## Logs (§18.1)

The gateway emits **contract-conformant JSON logs** (`internal/obs/log.go`) with all required fields:

| Field | Description |
|---|---|
| `timestamp` | RFC3339Nano UTC |
| `level` | debug / info / warn / error / fatal |
| `environment` | `UBAG_ENVIRONMENT` env var (default: `local`) |
| `service` | always `ubag-gateway` |
| `message` | log message text |
| `trace_id` | from W3C `traceparent` context |

**PII/PHI redaction** drops keys matching patterns from `packages/observability/src/safety.mjs` (passwords, tokens, raw_prompt, api_key, etc.). PHI records (`ClassPHI`) are dropped entirely.

**Log level hot-reload:** send `SIGHUP` or set `UBAG_LOG_LEVEL=debug`.

The Python worker emits the same JSON schema via `ubag_worker.obs.logging`.

## Metrics (§18.2)

Gateway `/v1/metrics` emits the full 22-metric contract from `packages/observability/src/metrics.mjs`. All metrics use only the bounded label names defined in the cardinality budget (≤6 labels per series, no job IDs or tenant IDs in labels).

Key counters tracked atomically:
- `ubag_idempotency_replays_total` — idempotency hit path
- `ubag_artifact_captures_total` — successful artifact writes
- `ubag_webhook_deliveries_total` — webhook delivery outcomes

**CI validation:** `node tools/check-metrics-cardinality.mjs` and `node tools/check-grafana-dashboards.mjs` run in every CI push.

## Traces (§18.3)

OpenTelemetry SDK is initialised in `internal/obs/otel.go` (`InitTracer`):
- **No endpoint:** no-op provider installed (zero external dependency)
- **With `UBAG_OTLP_ENDPOINT`:** OTLP/gRPC exporter to the configured collector

**Sampling:** `ParentBased(ErrorOrRatio{10%})` — 10% root spans, 100% error spans (call `obs.SampleError(ctx)` on error paths).

**Propagation:** W3C `traceparent` + `baggage`. Use `obs.WrapWithOTel(handler, name)` to wrap HTTP handlers with automatic span creation.

The Python worker reads `trace_context.traceparent` from the gateway dispatch envelope and continues the trace via `ubag_worker.obs.tracing`.

## Dashboards (§18.4)

9 Grafana dashboards auto-provisioned under the `observability` compose profile (`deploy/grafana/dashboards/`):

1. **Gateway HTTP** — request rate, p99 latency, inflight
2. **Jobs Lifecycle** — creation rate, state distribution, idempotency replays
3. **Queue Depth** — depth by state, oldest job age
4. **Worker Throughput** — processed/s, duration, ingestion rate, artifact captures
5. **Webhook Delivery** — attempts, latency, outbox depth
6. **Channel Pool / AIMD** — adapter requests and latency
7. **Cache Hit Rate** — idempotency replay fraction
8. **SSE / Streaming** — active SSE connections
9. **SLO Overview** — success rates (7d), error budget burn (1h windows)

## Synthetic Monitoring & SLOs (§18.5)

`tools/synthetic-monitor/monitor.mjs` periodically probes each adapter, records success/latency, and exposes `ubag_synthetic_*` Prometheus metrics including:
- `ubag_synthetic_slo_burn_rate` — ratio of current error rate to allowed error rate
- `ubag_synthetic_slo_error_rate`
- `ubag_synthetic_slo_success_rate`
- `ubag_synthetic_health` — 1=healthy, 0=degraded

### SLO definition

| Signal | Target | Window |
|---|---|---|
| Gateway availability | 99.9% | 30 days |
| Worker job success | 99.5% | 30 days |
| Webhook delivery | 99.0% | 30 days |

Burn rate alert threshold: >14.4× (exhausts 1-hour budget in 5 minutes).

The Go `FailureBudget` type (`internal/obs/slo.go`) implements the rolling-window math and is unit-tested.

## Profiling / Error Aggregation / Log Shipping (§18.6)

Enable with `docker compose --profile observability up`:

| Service | Image | Purpose |
|---|---|---|
| `pyroscope` | `grafana/pyroscope` | Continuous profiling (gateway pprof via `UBAG_PPROF_ADDR`) |
| `glitchtip` | `glitchtip/glitchtip` | Sentry-compatible error aggregation |
| `vector` | `timberio/vector` | JSON log scraping → Loki |

Gateway pprof endpoint is enabled when `UBAG_PPROF_ADDR` is set (e.g. `127.0.0.1:6060`).

`UBAG_PPROF_ADDR` accepts only a loopback host (`127.0.0.1`, `::1`, `localhost`). Unset means no listener; a non-loopback value (including `:6060`) is refused with a warning and the gateway keeps running without profiling. Only the `/debug/pprof/*` routes are served, and no new authentication surface is added.

### Go runtime and process metrics

`/v1/metrics` also emits the standard `go_*` and `process_*` series under their conventional (non-`ubag_`) names: `go_goroutines`, `go_threads`, `go_memstats_{heap_alloc,heap_inuse,sys}_bytes`, `go_memstats_alloc_bytes_total`, `go_gc_cycles_total`, `go_gc_pause_seconds_total`, `go_gc_last_pause_seconds`, `process_start_time_seconds`, and, on Linux (read from `/proc`), `process_cpu_seconds_total`, `process_resident_memory_bytes` and `process_open_fds`. The values come from the same 5-second cached render as the other series.
