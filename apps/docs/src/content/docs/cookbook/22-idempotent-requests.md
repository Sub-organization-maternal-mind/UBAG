---
title: Making Idempotent Requests
description: Use Idempotency-Key headers to safely retry UBAG mutations without duplicating side effects.
---

All UBAG mutation endpoints accept an `Idempotency-Key` header (UUID v4).
Retrying with the same key returns the original response without re-executing the operation.

## Required for mutations

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: 550e8400-e29b-41d4-a716-446655440000" \
  -d '{"job": {"target": "https://example.com", "command_type": "screenshot"}}'
```

If the first request succeeded but you never received the response, retry with the **same key**:

```bash
# Safe retry — same key, same response returned
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: 550e8400-e29b-41d4-a716-446655440000" \
  -d '{"job": {"target": "https://example.com", "command_type": "screenshot"}}'
# Returns the original job — no new job created
```

## TypeScript — auto-generated keys

The TypeScript SDK auto-generates idempotency keys:

```ts
import { UbagClient } from '@ubag/sdk';
import { randomUUID } from 'node:crypto';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

// SDK generates a key automatically
const job = await client.jobs.create({ target: 'https://example.com', commandType: 'screenshot' });

// Provide your own for deterministic retry correlation
const job2 = await client.jobs.create(
  { target: 'https://example.com', commandType: 'screenshot' },
  { idempotencyKey: randomUUID() }
);
```

## Key expiry

Idempotency keys are retained for **24 hours**. After expiry, the same key re-executes the operation.

## Conflict detection

If the same key is used with a **different** request body, the gateway returns `409 Conflict`.

See [Idempotency](/contracts/idempotency) for the full specification.
