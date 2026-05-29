# @ubag/observability

Executable observability, QA, and ops health contracts for UBAG.

## What This Owns

- Stable Prometheus metric names and safe label budgets.
- Structured event names using `domain.resource.action.outcome`.
- Structured log shape validation with privacy guardrails.
- Operator smoke checklist entries with required evidence.
- Health probe definitions for gateway, ingress, Prometheus, Grafana, and small profile smoke.

This package is intentionally separate from gateway, dashboard, and worker runtime internals. Runtime emitters can import these contracts later without changing the contract source.

## Commands

```powershell
cmd /c pnpm --filter @ubag/observability test
cmd /c pnpm --filter @ubag/observability validate
cmd /c pnpm --filter @ubag/observability print:smoke
cmd /c pnpm --filter @ubag/observability health
```

The `health` command runs HTTP probes against local defaults:

- Gateway: `http://127.0.0.1:8080`
- Ingress: `http://127.0.0.1:8081`
- Prometheus: `http://127.0.0.1:9090`
- Grafana: `http://127.0.0.1:3000`
- NATS monitor: `http://127.0.0.1:8222`
- MinIO API: `http://127.0.0.1:9000`

Override them with `UBAG_GATEWAY_BASE_URL`, `UBAG_INGRESS_BASE_URL`, `UBAG_PROMETHEUS_BASE_URL`, `UBAG_GRAFANA_BASE_URL`, `UBAG_NATS_MONITOR_BASE_URL`, and `UBAG_MINIO_API_BASE_URL`. Limit probes with `UBAG_HEALTH_PROBES`, for example:

```powershell
$env:UBAG_HEALTH_PROBES = "gateway.health,gateway.ready"
cmd /c pnpm --filter @ubag/observability health
```
