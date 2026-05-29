---
title: Observability
description: Logs, metrics, traces, profiling, error aggregation, and dashboards.
---

## Logs

Structured JSON logs include timestamp, level, service, trace ID, span ID, tenant, app, job, target, adapter, and message. PII redaction applies before emission where configured.

## Metrics

Prometheus metrics follow RED for endpoints and USE for resources. Cardinality budgets forbid per-job labels.

Gateway `/v1/metrics` emits queue/executor readiness checks plus `ubag_queue_depth`, `ubag_queue_oldest_job_age_seconds`, `ubag_worker_jobs_processed_total`, `ubag_worker_job_duration_seconds`, `ubag_worker_result_ingestions_total`, and `ubag_worker_result_ingestion_duration_seconds`. In the local file-spool bridge, these values are derived from gateway job/spool state and result-ingestion outcomes; production worker fleets should back the same metric families with durable stores.

Webhook readiness and backlog are exposed through
`ubag_gateway_ready{check="webhooks"}`, `ubag_webhook_outbox_depth`, and
`ubag_webhook_outbox_oldest_age_seconds`. Labels stay bounded to
`endpoint_kind` and delivery `state`; delivery IDs, callback URLs, tenant IDs,
and app IDs are intentionally excluded from metric labels.

## Traces

The current contracts reserve W3C trace context fields across SDK, gateway, worker, adapter, normalization, and webhook dispatch. Full OpenTelemetry export is a production hardening item.

## Dashboards

The small profile ships Prometheus/Grafana scaffolding and metric registries for gateway, queue, worker ingestion, and webhook outbox health. Production dashboards for per-target performance, tenant usage, cache hit rate, and session-pool health are planned hardening work.

## Synthetic monitoring

The `@ubag/observability` health runner probes gateway, Caddy ingress,
Prometheus, Grafana, NATS monitor, and MinIO live endpoints. Override local
defaults with `UBAG_GATEWAY_BASE_URL`, `UBAG_INGRESS_BASE_URL`,
`UBAG_PROMETHEUS_BASE_URL`, `UBAG_GRAFANA_BASE_URL`,
`UBAG_NATS_MONITOR_BASE_URL`, and `UBAG_MINIO_API_BASE_URL`.

Provider canaries require user-owned accounts and live manual sessions. The current repository keeps synthetic/provider canary behavior as an activation item rather than an always-on runtime.
