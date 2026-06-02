---
title: Manage Webhook Endpoints
description: Register, list, update, and delete UBAG webhook endpoints.
---

UBAG delivers job lifecycle events to your webhook endpoints via HTTPS POST.

## Register a webhook

```bash
curl -X POST http://localhost:8081/v1/webhooks \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "url": "https://my-app.example.com/webhooks/ubag",
    "events": ["job.completed", "job.failed", "job.cancelled"],
    "secret": "my-webhook-signing-secret"
  }'
```

Response:

```json
{ "id": "wh-abc", "url": "https://...", "events": ["job.completed", "job.failed", "job.cancelled"] }
```

## List webhooks

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/webhooks
```

## Update a webhook

```bash
curl -X PATCH http://localhost:8081/v1/webhooks/wh-abc \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"events": ["job.completed", "job.failed"]}'
```

## Delete a webhook

```bash
curl -X DELETE http://localhost:8081/v1/webhooks/wh-abc \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22"
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const webhook = await client.webhooks.create({
  url: 'https://my-app.example.com/webhooks/ubag',
  events: ['job.completed', 'job.failed'],
  secret: process.env.WEBHOOK_SECRET!,
});
console.log('Webhook ID:', webhook.id);
```

## Delivery retries

UBAG retries failed deliveries with exponential backoff (max 5 attempts over 24h).
See [Webhooks](/contracts/webhooks) for the delivery guarantee and payload schema.
For signature verification, see [Verify a Webhook](/cookbook/03-verify-webhook).
