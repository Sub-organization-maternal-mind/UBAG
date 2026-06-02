---
title: Monitor Gateway Health
description: Check the UBAG gateway health endpoint and set up monitoring alerts.
---

The gateway exposes `/v1/health` for readiness probes and monitoring integrations.

## Basic health check

```bash
curl http://localhost:8081/v1/health
```

Response:

```json
{
  "status": "ok",
  "version": "0.9.0",
  "checks": {
    "postgres": "ok",
    "nats": "ok",
    "worker_pool": "ok",
    "artifact_store": "ok"
  },
  "uptime_seconds": 86400
}
```

A non-`"ok"` top-level `status` means the gateway is degraded. The `checks` map
shows which subsystem is failing.

## Kubernetes probe

```yaml
livenessProbe:
  httpGet:
    path: /v1/health
    port: 8081
  initialDelaySeconds: 10
  periodSeconds: 30
readinessProbe:
  httpGet:
    path: /v1/health
    port: 8081
  initialDelaySeconds: 5
  periodSeconds: 10
```

## Prometheus scrape

```bash
curl http://localhost:8081/v1/metrics
# ubag_gateway_up 1
# ubag_job_queue_depth 3
# ubag_worker_pool_active 2
```

## Alert rules (Prometheus)

```yaml
groups:
  - name: ubag
    rules:
      - alert: UbagGatewayDown
        expr: ubag_gateway_up == 0
        for: 1m
        labels: { severity: critical }
        annotations:
          summary: "UBAG Gateway is down"

      - alert: UbagHighQueueDepth
        expr: ubag_job_queue_depth > 100
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "UBAG job queue depth is high: {{ $value }}"
```

## TypeScript health check

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });
const health = await client.health.check();
if (health.status !== 'ok') {
  console.error('Gateway degraded:', health.checks);
}
```

See [Observability](/operations/observability) for the full metrics and alerting reference.
See [Runbook](/operations/runbook) for on-call response procedures.
