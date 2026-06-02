---
title: Install a Plugin
description: Install a WASM plugin into the UBAG gateway to extend adapter or middleware behavior.
---

UBAG plugins are WebAssembly modules loaded at worker startup. They can intercept job
lifecycle hooks, transform inputs/outputs, or add custom adapters.

## Upload a plugin

```bash
curl -X POST http://localhost:8081/v1/plugins \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)" \
  -F "file=@./my-plugin.wasm" \
  -F "metadata={\"id\":\"my-plugin\",\"version\":\"1.0.0\",\"hooks\":[\"before_job\",\"after_job\"]}"
```

## List installed plugins

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/plugins
```

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';
import { readFileSync } from 'node:fs';

const client = new UbagClient({ gatewayUrl: '...', appSecret: process.env.UBAG_APP_SECRET, apiVersion: '2026-05-22' });

const wasm = readFileSync('./my-plugin.wasm');
const plugin = await client.plugins.install({
  id: 'my-plugin',
  version: '1.0.0',
  wasm,
  hooks: ['before_job', 'after_job'],
});
console.log('Plugin installed:', plugin.id);
```

## Enable a plugin for a specific app

```bash
curl -X PATCH http://localhost:8081/v1/apps/$APP_ID \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"plugins": ["my-plugin"]}'
```

See [Plugin Authoring](/cookbook/27-plugin-authoring) for how to write plugins.
See [Plugins](/plugins) for the full plugin system reference.
