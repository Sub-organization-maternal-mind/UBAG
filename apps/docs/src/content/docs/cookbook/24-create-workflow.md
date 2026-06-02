---
title: Define and Trigger a Workflow
description: Create multi-step workflows that chain UBAG jobs together with conditional branching.
---

Workflows let you define directed graphs of jobs where outputs of one job feed inputs of the next.

## Define a workflow

```bash
curl -X POST http://localhost:8081/v1/workflows \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "id": "summarize-and-post",
    "steps": [
      {
        "id": "scrape",
        "job": { "target": "https://news.example.com", "command_type": "extract_text" }
      },
      {
        "id": "summarize",
        "depends_on": ["scrape"],
        "job": {
          "target": "https://chat.openai.com",
          "command_type": "send_message",
          "input": { "prompt": "Summarize: {{scrape.output.text}}" }
        }
      }
    ]
  }'
```

## List workflows

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/workflows
```

## Trigger a workflow

```bash
curl -X POST http://localhost:8081/v1/workflows/summarize-and-post:trigger \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"vars": {"article_url": "https://news.example.com/article/123"}}'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const run = await client.workflows.trigger('summarize-and-post', {
  vars: { article_url: 'https://news.example.com/article/123' },
});
console.log('Workflow run ID:', run.id);

// Poll for completion
for await (const event of client.workflows.stream(run.id)) {
  console.log('Step:', event.stepId, 'Status:', event.status);
  if (event.workflowStatus === 'DONE') break;
}
```

See [Job Contract](/contracts/job-contract) for how workflow steps inherit job semantics.
