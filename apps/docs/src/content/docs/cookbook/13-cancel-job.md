---
title: Cancel a Running Job
description: Stop a queued or running job before it completes.
---

Cancel a job via `POST /v1/jobs/{id}:cancel`.

## curl

```bash
curl -X POST http://localhost:8081/v1/jobs/$JOB_ID:cancel \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
```

Response:

```json
{ "id": "abc-123", "status": "JOB_STATUS_CANCELLED", "cancelled_at": "2026-05-22T10:05:00Z" }
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });
const result = await client.jobs.cancel(jobId);
console.log('Status:', result.status); // JOB_STATUS_CANCELLED
```

## Cancellation semantics

- Cancellations are **best-effort** for jobs already running (the worker receives a signal and stops at the next checkpoint).
- Queued jobs are cancelled immediately.
- Completed or already-cancelled jobs return `200` (idempotent).
- The SSE stream for a cancelled job emits a final `JOB_STATUS_CANCELLED` event and closes.

## Bulk cancel

Cancel all running jobs for an app (admin only):

```bash
curl -X POST http://localhost:8081/v1/jobs:bulk-cancel \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"filter": {"status": "JOB_STATUS_RUNNING", "app_id": "my-app"}}'
```

See [Job Lifecycle](/contracts/job-lifecycle) for the full state machine.
