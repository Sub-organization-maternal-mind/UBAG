---
title: Understand and Handle Rate Limits
description: How UBAG rate limits work, how to read the headers, and how to handle 429 responses.
---

UBAG enforces per-actor rate limits at the gateway. Exceeding limits returns `429 Too Many Requests`.

## Rate limit headers

Every response includes:

```
X-Ratelimit-Limit: 100
X-Ratelimit-Remaining: 73
X-Ratelimit-Reset: 1748000060
Retry-After: 12
```

## Default limits

| Endpoint group | Limit | Window |
|---------------|-------|--------|
| Job creation | 100/min | Rolling |
| Job reads | 1000/min | Rolling |
| Webhook management | 20/min | Rolling |
| Audit log reads | 200/min | Rolling |

Limits are per `app_id`. Contact support to increase limits for production workloads.

## Check current limits

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/rate-limits
```

## TypeScript — handle 429

```ts
import { UbagClient, UbagRateLimitError } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

async function createJobWithRetry(spec: object, maxRetries = 3) {
  for (let attempt = 0; attempt < maxRetries; attempt++) {
    try {
      return await client.jobs.create(spec);
    } catch (err) {
      if (err instanceof UbagRateLimitError) {
        const delay = err.retryAfterMs ?? 5000 * (attempt + 1);
        console.warn(`Rate limited. Retrying in ${delay}ms...`);
        await new Promise(r => setTimeout(r, delay));
      } else throw err;
    }
  }
  throw new Error('Max retries exceeded');
}
```

## Backoff strategy

Use exponential backoff with jitter. The `Retry-After` header gives the minimum wait in seconds.

See [Error Catalog](/contracts/error-catalog) for the `rate_limit_exceeded` error type.
