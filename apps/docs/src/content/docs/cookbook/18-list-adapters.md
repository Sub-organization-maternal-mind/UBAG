---
title: List Available Adapters
description: Discover which AI provider adapters are registered in the UBAG gateway.
---

Adapters translate `command_type` job specs into concrete browser actions for a given AI provider.

## List adapters

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/adapters
```

Response:

```json
{
  "adapters": [
    {
      "id": "openai-chatgpt",
      "provider": "openai",
      "version": "2.1.0",
      "status": "healthy",
      "commands": ["send_message", "create_image", "upload_file"],
      "last_drift_check": "2026-05-22T09:00:00Z",
      "drift_detected": false
    },
    {
      "id": "anthropic-claude",
      "provider": "anthropic",
      "version": "1.4.0",
      "status": "healthy",
      "commands": ["send_message", "create_artifact"]
    }
  ]
}
```

## Get a specific adapter

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/adapters/openai-chatgpt
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const { adapters } = await client.adapters.list();
const healthy = adapters.filter(a => a.status === 'healthy');
console.log(`${healthy.length} healthy adapters`);
```

## Filter by status

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/adapters?status=degraded"
```

See [AI Provider Rollout](/adapters/ai-provider-rollout) and [Drift Detection](/adapters/drift-detection).
