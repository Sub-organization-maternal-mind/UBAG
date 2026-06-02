---
title: Purge the Semantic Cache
description: Invalidate cached job results in the UBAG semantic cache layer.
---

The semantic cache stores deduplicated job outputs keyed by a hash of the job spec.
Purge it when a target's content changes or you need fresh results.

## Purge all cache entries

```bash
curl -X DELETE http://localhost:8081/v1/cache \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
```

## Purge by target

```bash
curl -X DELETE "http://localhost:8081/v1/cache?target=https://chat.openai.com" \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
```

## Purge by adapter

```bash
curl -X DELETE "http://localhost:8081/v1/cache?adapter=openai-chatgpt" \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
```

## Bypass cache for a single job

Add `cache_policy: "bypass"` to disable cache lookup for one request:

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"job": {"target": "...", "command_type": "screenshot", "cache_policy": "bypass"}}'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

await client.cache.purge({ target: 'https://chat.openai.com' });
console.log('Cache purged for target');
```

## Cache stats

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/cache/stats
```

See [Observability](/operations/observability) for cache hit-rate metrics in Prometheus/Grafana.
