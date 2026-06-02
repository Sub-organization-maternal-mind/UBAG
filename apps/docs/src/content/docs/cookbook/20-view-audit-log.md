---
title: View and Verify the Audit Log
description: Query the UBAG audit log and verify the hash chain integrity.
---

Every mutation in UBAG is recorded in a hash-chained audit log accessible at `/v1/audit`.

## Query recent entries

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/audit?limit=20&order=desc"
```

Response:

```json
{
  "entries": [
    {
      "id": "audit-001",
      "actor": "app:my-app",
      "action": "job.created",
      "resource": "job:abc-123",
      "occurred_at": "2026-05-22T10:00:00Z",
      "hash": "sha256:abc...",
      "prev_hash": "sha256:xyz..."
    }
  ]
}
```

## Filter by actor or action

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/audit?actor=app:my-app&action=job.created&since=2026-05-01"
```

## Verify hash chain integrity

```bash
ubag-cli audit verify \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET
# Verifying 10,000 entries... OK — chain intact, no tampering detected
```

## Export to SIEM

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/audit/export?format=ndjson&since=2026-05-01" \
  | gzip > audit-export.ndjson.gz
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const { entries } = await client.audit.list({ limit: 50, order: 'desc' });
const valid = await client.audit.verifyChain(entries);
console.log('Chain valid:', valid);
```

See [Audit and Secrets](/security/audit-secrets) and [Audit Export and Merkle Chain](/security/audit-export-merkle).
