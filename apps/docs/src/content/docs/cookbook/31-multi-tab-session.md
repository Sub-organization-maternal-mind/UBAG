---
title: Multi-Tab Browser Session
description: Run a job that opens and orchestrates multiple browser tabs in a single session.
---

UBAG workers support multi-tab orchestration for jobs that need to coordinate across
several pages simultaneously.

## Job input

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "job": {
      "target": "https://chat.openai.com",
      "command_type": "multi_tab_session",
      "input": {
        "tabs": [
          { "url": "https://chat.openai.com", "role": "primary" },
          { "url": "https://docs.example.com", "role": "reference" }
        ],
        "steps": [
          { "tab": "reference", "action": "extract_text", "selector": ".content" },
          { "tab": "primary",   "action": "send_message", "input_from": "reference.text" }
        ]
      }
    }
  }'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const job = await client.jobs.create({
  target: 'https://chat.openai.com',
  commandType: 'multi_tab_session',
  input: {
    tabs: [
      { url: 'https://chat.openai.com', role: 'primary' },
      { url: 'https://docs.example.com', role: 'reference' },
    ],
    steps: [
      { tab: 'reference', action: 'extract_text', selector: '.content' },
      { tab: 'primary',   action: 'send_message', inputFrom: 'reference.text' },
    ],
  },
});
```

## Observe tab events

SSE events include a `tab_id` field for multi-tab jobs:

```json
{
  "jobId": "abc-123",
  "status": "JOB_STATUS_RUNNING",
  "browser": { "tabId": "tab-primary", "event": "page_loaded", "url": "https://chat.openai.com" }
}
```

## noVNC live view

In dev/staging deployments, watch the browser session live:

```bash
# Open noVNC in browser
open http://localhost:7900/?password=ubag
```

See [Multi-Tab Orchestration](/worker/multi-tab-orchestration) for the full tab coordination protocol.
See [Sessions and noVNC](/worker/sessions-novnc) for the VNC debug setup.
