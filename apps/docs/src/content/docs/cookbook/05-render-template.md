---
title: Render a Template
description: Fetch and render a reusable job template from the UBAG template registry.
---

Templates let you define parameterized job blueprints and reuse them across submissions.

## List available templates

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/templates
```

## Render a template

```bash
curl -X POST http://localhost:8081/v1/templates/send-message/render \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{ "vars": { "prompt": "Summarize this page", "target": "https://example.com" } }'
```

Response contains a ready-to-submit job spec:

```json
{
  "job": {
    "target": "https://example.com",
    "command_type": "send_message",
    "input": { "prompt": "Summarize this page" }
  }
}
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const spec = await client.templates.render('send-message', {
  vars: { prompt: 'Summarize this page', target: 'https://example.com' },
});

const job = await client.jobs.create(spec.job);
console.log('Job ID:', job.id);
```

## Creating a template

Post to `/v1/templates` with a job spec and variable placeholders:

```bash
curl -X POST http://localhost:8081/v1/templates \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "send-message",
    "spec": {
      "target": "{{target}}",
      "command_type": "send_message",
      "input": { "prompt": "{{prompt}}" }
    }
  }'
```
