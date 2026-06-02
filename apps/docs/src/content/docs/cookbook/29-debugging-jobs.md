---
title: Debug Failed Jobs
description: Diagnose and fix failed UBAG jobs using logs, artifacts, and the debug API.
---

When a job fails, use the job detail endpoint, artifact logs, and the debug console to investigate.

## 1. Fetch job details

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/jobs/$JOB_ID
```

Check the `error` field:

```json
{
  "id": "abc-123",
  "status": "JOB_STATUS_FAILED",
  "error": {
    "code": "adapter_timeout",
    "message": "Adapter did not respond within 30s",
    "adapter": "openai-chatgpt",
    "step": "wait_for_response"
  }
}
```

## 2. Fetch job logs

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/jobs/$JOB_ID/artifacts?kind=logs" | jq '.'
```

## 3. Fetch screenshots / HAR

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     "http://localhost:8081/v1/jobs/$JOB_ID/artifacts?kind=screenshot"
# Returns pre-signed URLs valid for 15 minutes
```

## 4. Replay in debug mode

```bash
curl -X POST http://localhost:8081/v1/jobs/$JOB_ID:debug-replay \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
# Replays job with full CDP trace + headful browser if noVNC is available
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const job = await client.jobs.get(jobId);
console.log('Error:', job.error);

const artifacts = await client.jobs.listArtifacts(jobId);
for (const a of artifacts) console.log(a.kind, a.url);
```

## Common failure codes

| Code | Cause | Fix |
|------|-------|-----|
| `adapter_timeout` | Adapter slow/unresponsive | Increase `timeout_ms`; check adapter health |
| `browser_crash` | OOM or CDP disconnect | Increase worker memory; reduce concurrency |
| `target_blocked` | Site blocked scraping | Check cookies/login state; rotate session |
| `schema_mismatch` | Adapter drift detected | Update adapter to match site changes |

See [Drift Detection](/adapters/drift-detection) and [Error Catalog](/contracts/error-catalog).
