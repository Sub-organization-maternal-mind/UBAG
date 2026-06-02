---
title: Stream SSE Results
description: Follow job progress in real time using Server-Sent Events from the UBAG gateway.
---

The gateway streams job events over SSE at `/v1/jobs/{id}/events`.

## TypeScript (EventSource)

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({
  gatewayUrl: 'http://localhost:8081',
  appSecret: process.env.UBAG_APP_SECRET,
  apiVersion: '2026-05-22',
});

const job = await client.jobs.create({ target: 'https://example.com', commandType: 'screenshot' });

for await (const event of client.jobs.stream(job.id)) {
  if (event.log) console.log(`[${event.log.level}] ${event.log.body}`);
  if (event.artifact) console.log('Artifact:', event.artifact.url);
  if (event.status === 'JOB_STATUS_DONE') break;
}
```

## curl

```bash
curl -N \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  http://localhost:8081/v1/jobs/$JOB_ID/events
```

## Go

```go
stream, err := client.Jobs.Stream(ctx, jobID)
if err != nil { log.Fatal(err) }
defer stream.Close()

for event := range stream.Events() {
    fmt.Printf("status=%s\n", event.Status)
    if event.Status == ubag.JobStatusDone { break }
}
```

## Event shape

Each SSE frame carries a JSON-encoded `JobEvent`:

```json
{
  "jobId": "abc-123",
  "status": "JOB_STATUS_RUNNING",
  "occurredAt": "2026-05-22T10:00:01Z",
  "log": { "level": "info", "source": "adapter", "body": "Page loaded" }
}
```

See [Proto Reference](/reference/proto) for the full message schema.
