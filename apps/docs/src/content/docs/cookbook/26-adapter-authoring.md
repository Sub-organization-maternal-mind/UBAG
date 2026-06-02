---
title: Author a Custom Adapter
description: Write a UBAG adapter to support a new AI provider or web application target.
---

Adapters implement the UBAG `Adapter` trait and translate `command_type` specs into
browser actions using Playwright or a remote WebDriver grid.

## Scaffold

```bash
ubag-cli adapter new --name my-provider --lang typescript
# Creates: adapters/my-provider/
#   src/index.ts
#   src/commands/send_message.ts
#   ubag-adapter.json
#   package.json
```

## Adapter interface (TypeScript)

```ts
import type { AdapterContext, JobSpec, JobResult } from '@ubag/adapter-sdk';

export default class MyProviderAdapter {
  // Declare which command_types this adapter handles
  static commands = ['send_message', 'create_image'];

  async execute(spec: JobSpec, ctx: AdapterContext): Promise<JobResult> {
    const { page } = ctx.browser; // Playwright page

    await page.goto(spec.target);
    await page.waitForLoadState('networkidle');

    // Implement command
    if (spec.commandType === 'send_message') {
      await page.fill('#prompt', spec.input.prompt);
      await page.click('button[type=submit]');
      await page.waitForSelector('.response', { timeout: 30_000 });
      const text = await page.textContent('.response');
      return { output: { text }, status: 'success' };
    }

    throw new Error(`Unsupported command: ${spec.commandType}`);
  }
}
```

## Adapter manifest (`ubag-adapter.json`)

```json
{
  "id": "my-provider",
  "version": "1.0.0",
  "provider": "my-provider",
  "commands": ["send_message", "create_image"],
  "targets": ["https://my-provider.example.com"],
  "runtime": "typescript"
}
```

## Register the adapter

```bash
ubag-cli adapter register \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET \
  --dir ./adapters/my-provider
```

## Test the adapter

```bash
ubag-cli adapter test \
  --adapter my-provider \
  --command send_message \
  --input '{"prompt": "Hello"}'
```

See [Adapter Contract](/adapters/contract) for the full interface specification.
See [Provider Rollout](/adapters/provider-rollout) for the gradual rollout process.
