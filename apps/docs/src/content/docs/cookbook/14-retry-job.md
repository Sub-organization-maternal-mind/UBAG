---
title: Retry a Failed Job
description: Resubmit a failed job with the same or modified input.
---

Retry a failed job by re-creating it, optionally inheriting the original job's inputs.

## curl — same inputs

```bash
curl -X POST http://localhost:8081/v1/jobs/$FAILED_JOB_ID:retry \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
```

Returns a new job ID. The original job is linked as the retry parent.

## curl — with modified inputs

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "job": {
      "target": "https://example.com",
      "command_type": "send_message",
      "input": { "prompt": "Try again with different input" },
      "retry_of": "abc-123"
    }
  }'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

// Simple retry
const retried = await client.jobs.retry(failedJobId);
console.log('New job ID:', retried.id);

// Retry with new inputs
const retried2 = await client.jobs.create({
  target: 'https://example.com',
  commandType: 'send_message',
  input: { prompt: 'Updated prompt' },
  retryOf: failedJobId,
});
```

## Automatic retry policy

Configure per-app retry policies:

```bash
curl -X PATCH http://localhost:8081/v1/apps/$APP_ID \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"retry_policy": {"max_attempts": 3, "backoff": "exponential", "initial_delay_ms": 1000}}'
```

See [Job Contract](/contracts/job-contract) for retry semantics and lineage tracking.
