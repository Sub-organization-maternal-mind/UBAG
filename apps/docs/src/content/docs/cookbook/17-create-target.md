---
title: Create a Custom Target
description: Register a custom browser-automation target (site profile) in the UBAG gateway.
---

Targets are named site profiles that bundle credentials, selectors, and behavioral hints
for a specific website. Create them once and reference them by name in job submissions.

## Create a target

```bash
curl -X POST http://localhost:8081/v1/targets \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "id": "my-crm",
    "base_url": "https://crm.example.com",
    "auth": {
      "type": "form_login",
      "username_selector": "#email",
      "password_selector": "#password",
      "submit_selector": "button[type=submit]",
      "credentials": {
        "source": "secret",
        "secret_name": "crm-credentials"
      }
    },
    "hints": {
      "spa": true,
      "wait_for_network_idle": true
    }
  }'
```

## List targets

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/targets
```

## Reference a target in a job

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"job": {"target": "my-crm", "command_type": "extract_data", "input": {"selector": ".contact-list"}}}'
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

await client.targets.create({
  id: 'my-crm',
  baseUrl: 'https://crm.example.com',
  auth: { type: 'form_login', usernameSelectorr: '#email', passwordSelector: '#password' },
});
```

See [Adapter Contract](/adapters/contract) for how adapters consume target definitions.
