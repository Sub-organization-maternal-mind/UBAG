---
title: Configure SIEM Integration
description: Forward UBAG audit events to a SIEM (Splunk, Elastic, Datadog) for security monitoring.
---

UBAG can forward audit events to any SIEM via webhook, direct API push, or log export.

## Webhook-based SIEM forwarding

Register a webhook that POSTs to your SIEM ingestion endpoint:

```bash
curl -X POST http://localhost:8081/v1/webhooks \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "url": "https://http-inputs.splunkcloud.example.com/services/collector",
    "events": ["audit.*"],
    "headers": { "Authorization": "Splunk $SPLUNK_HEC_TOKEN" },
    "format": "splunk_hec"
  }'
```

## Datadog

```bash
curl -X POST http://localhost:8081/v1/siem/integrations \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "provider": "datadog",
    "config": {
      "api_key": "$DATADOG_API_KEY",
      "site": "datadoghq.com",
      "service": "ubag-gateway",
      "source": "ubag"
    }
  }'
```

## Elastic (via Filebeat)

Add to `filebeat.yml`:

```yaml
inputs:
  - type: http_endpoint
    listen_address: "0.0.0.0:8088"
    paths:
      - /ubag-audit

output.elasticsearch:
  hosts: ["https://es.example.com:9200"]
  index: "ubag-audit-%{+yyyy.MM.dd}"
```

Then register UBAG webhook targeting `http://filebeat-host:8088/ubag-audit`.

## Ndjson export (batch)

For periodic SIEM ingestion, export as NDJSON:

```bash
ubag-cli audit export \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET \
  --since 1h \
  --format ndjson \
  | curl -X POST https://siem.example.com/ingest \
    -H "Content-Type: application/x-ndjson" \
    --data-binary @-
```

## Event filtering

Configure which audit actions to forward to SIEM:

```bash
curl -X PATCH http://localhost:8081/v1/siem/integrations/$INTEGRATION_ID \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"event_filter": ["job.created", "job.failed", "secret.rotated", "user.login", "user.mfa_enrolled"]}'
```

See [Audit and Secrets](/security/audit-secrets) for event schemas.
See [Audit Export and Merkle Chain](/security/audit-export-merkle) for tamper-evident export.
