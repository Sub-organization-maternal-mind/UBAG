---
title: App Developer Guide
description: How to build applications that use the UBAG gateway to automate browser interactions.
---

This guide covers integrating the UBAG SDK into your application, from authentication
to streaming results and handling errors in production.

## Setup

Install the TypeScript SDK:

```bash
npm install @ubag/sdk
```

Or use the Go SDK:

```bash
go get github.com/ubag/sdk-go
```

## Authentication

All requests require:
- `Authorization: Bearer <app_secret>` — your app's secret key
- `Ubag-Api-Version: 2026-05-22` — the API version header

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({
  gatewayUrl: process.env.UBAG_GATEWAY_URL ?? 'http://localhost:8081',
  appSecret: process.env.UBAG_APP_SECRET,
  apiVersion: '2026-05-22',
});
```

Never commit `UBAG_APP_SECRET` to source control. Use environment variables or a secrets manager.

## Submitting jobs

See the [Submit a Job](/cookbook/01-submit-job) recipe for full language examples.

Basic pattern:

```ts
const job = await client.jobs.create({
  target: 'https://chat.openai.com',
  commandType: 'send_message',
  input: { prompt: 'Summarize this article' },
});
console.log('Job ID:', job.id);
```

## Streaming results

Job progress arrives via SSE. Stream it with the SDK:

```ts
for await (const event of client.jobs.stream(job.id)) {
  if (event.log) console.log(event.log.body);
  if (event.artifact) console.log('Artifact:', event.artifact.url);
  if (event.status === 'JOB_STATUS_DONE') break;
}
```

See [Stream Results](/cookbook/02-stream-results) for details.

## Idempotency

Always supply an `Idempotency-Key` for mutations. The SDK handles this automatically.
See [Making Idempotent Requests](/cookbook/22-idempotent-requests).

## Error handling

The SDK throws typed errors:

```ts
import { UbagDeniedError, UbagRateLimitError, UbagNotFoundError } from '@ubag/sdk';

try {
  const job = await client.jobs.create(spec);
} catch (err) {
  if (err instanceof UbagRateLimitError) {
    // Retry after err.retryAfterMs
  } else if (err instanceof UbagDeniedError) {
    // Check app secret and permissions
  } else {
    throw err;
  }
}
```

See [Error Catalog](/contracts/error-catalog) for all error types.

## Webhooks

Register a webhook to receive job events asynchronously instead of polling or streaming:

```ts
await client.webhooks.create({
  url: 'https://my-app.example.com/webhooks/ubag',
  events: ['job.completed', 'job.failed'],
  secret: process.env.WEBHOOK_SECRET,
});
```

See [Manage Webhooks](/cookbook/21-manage-webhooks) and [Verify a Webhook](/cookbook/03-verify-webhook).

## Rate limits

See [Rate Limits](/cookbook/23-rate-limits) for header documentation and retry patterns.

## SDK conformance

The SDK is conformance-tested against the gateway contract.
See [SDK Conformance](/contracts/sdk-conformance) for the test suite.
