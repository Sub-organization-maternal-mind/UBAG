---
title: Adapter Author Guide
description: End-to-end guide for writing, testing, and publishing a UBAG adapter for a new AI provider or web target.
---

Adapters are the translation layer between UBAG job specs and concrete browser actions.
Each adapter handles one or more `command_type` values for a specific provider.

## Prerequisites

- Node.js 20+ or Go 1.22+
- Playwright 1.44+ (`npx playwright install chromium`)
- Access to the target site (credentials if needed)

## Concepts

| Term | Meaning |
|------|---------|
| `command_type` | Named operation (e.g. `send_message`, `screenshot`) |
| Provider | The target site or AI service (e.g. `openai`, `anthropic`) |
| Adapter manifest | `ubag-adapter.json` — declares supported commands and metadata |
| `AdapterContext` | Runtime context passed to your handler: `page`, `logger`, `artifacts` |

## Scaffold

```bash
ubag-cli adapter new --name my-provider --lang typescript
cd adapters/my-provider
npm install
```

## Implement the adapter

```ts
// src/index.ts
import type { AdapterContext, JobSpec, JobResult } from '@ubag/adapter-sdk';

export default class MyProviderAdapter {
  static commands = ['send_message'];

  async execute(spec: JobSpec, ctx: AdapterContext): Promise<JobResult> {
    const { page, logger, artifacts } = ctx;

    await page.goto(spec.target);
    logger.info('Page loaded', { url: spec.target });

    await page.fill('#prompt', spec.input.prompt);
    await page.click('button[type=submit]');

    await page.waitForSelector('.response', { timeout: 30_000 });
    const text = await page.textContent('.response');

    const screenshot = await artifacts.screenshot('response');
    return { output: { text }, artifacts: [screenshot], status: 'success' };
  }
}
```

## Test locally

```bash
ubag-cli adapter test \
  --adapter ./adapters/my-provider \
  --command send_message \
  --input '{"prompt": "Hello"}'
```

## Conformance tests

Run the adapter conformance suite:

```bash
ubag-cli adapter conformance ./adapters/my-provider
```

All adapters must pass before registration.

## Register

```bash
ubag-cli adapter register \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET \
  --dir ./adapters/my-provider
```

## Drift detection

The CI pipeline runs drift checks to alert when the target site changes selectors.
See [Drift Detection](/adapters/drift-detection).

## Rolling out

New adapters are rolled out in shadow mode first (traffic mirrored, not live).
See [Provider Rollout](/adapters/provider-rollout).

## Further reading

- [Adapter Contract](/adapters/contract) — full interface spec
- [Author a Custom Adapter](/cookbook/26-adapter-authoring) — cookbook recipe
