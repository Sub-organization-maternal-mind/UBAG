---
title: List Browser Sessions
description: Inspect active and recent browser sessions managed by the UBAG worker.
---

Browser sessions are ephemeral Chromium/Firefox instances spun up per job.
Query them via `/v1/browser/instances`.

## List active sessions

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/browser/instances
```

Response:

```json
{
  "instances": [
    {
      "id": "browser-abc",
      "job_id": "job-123",
      "worker_id": "worker-1",
      "engine": "chromium",
      "tab_count": 2,
      "started_at": "2026-05-22T10:00:00Z",
      "memory_mb": 245
    }
  ],
  "total": 1
}
```

## Session summary

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/browser/summary
```

Returns aggregate stats: total sessions, memory usage, engine distribution.

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const { instances } = await client.browser.listInstances();
for (const inst of instances) {
  console.log(`${inst.id}: job=${inst.jobId} tabs=${inst.tabCount} engine=${inst.engine}`);
}
```

## Filter by worker

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/browser/instances?worker_id=worker-1"
```

See [Worker Sessions](/worker/sessions) for session lifecycle details.
See [Multi-Tab Orchestration](/worker/multi-tab-orchestration) for tab management.
