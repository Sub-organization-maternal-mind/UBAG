---
title: Pin a Job to a Region
description: Constrain job execution to a specific geographic region for compliance or latency requirements.
---

In enterprise multi-region deployments, you can pin individual jobs to a specific region.

## Via job input

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "job": {
      "target": "https://example.com",
      "command_type": "screenshot",
      "region_affinity": { "region": "eu-west-1", "strict": true }
    }
  }'
```

Setting `strict: true` causes the job to fail (not fall back) if the target region is unavailable.
Setting `strict: false` falls back to any available region.

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const job = await client.jobs.create({
  target: 'https://example.com',
  commandType: 'screenshot',
  regionAffinity: { region: 'eu-west-1', strict: true },
});
```

## App-level default region

Set a default region for all jobs submitted by an app:

```bash
curl -X PATCH http://localhost:8081/v1/apps/$APP_ID \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"default_region": "eu-west-1"}'
```

## List available regions

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/regions
```

See [Multi-Region Operations](/operations/multi-region) for region topology and GeoDNS routing.
