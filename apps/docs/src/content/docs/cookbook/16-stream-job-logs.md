---
title: Stream Job Logs via SSE
description: Tail live log output from a running UBAG job using Server-Sent Events.
---

Job log lines are streamed as `JobEvent` frames with the `log` field populated.

## curl

```bash
# Stream all events (logs + status + artifacts)
curl -N \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  "http://localhost:8081/v1/jobs/$JOB_ID/events"
```

To filter only log lines, pipe through `jq`:

```bash
curl -N \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  "http://localhost:8081/v1/jobs/$JOB_ID/events" | \
  grep '^data:' | jq -r 'select(.log) | "[\(.log.level)] \(.log.body)"'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

for await (const event of client.jobs.stream(jobId)) {
  if (event.log) {
    console.log(`[${event.log.level}] [${event.log.source}] ${event.log.body}`);
  }
  if (['JOB_STATUS_DONE', 'JOB_STATUS_FAILED', 'JOB_STATUS_CANCELLED'].includes(event.status)) {
    break;
  }
}
```

## Historical logs

Retrieve logs for a completed job from the artifact store:

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/jobs/$JOB_ID/artifacts?kind=logs"
```

See [Artifact Capture](/worker/artifact-capture) for log retention settings.
See [Proto Reference](/reference/proto) for the `LogLine` message schema.
