---
title: Submit a Job
description: How to submit a job to the UBAG gateway using the TypeScript SDK, Go SDK, or curl.
---

Submit a browser-automation job via the `/v1/jobs` endpoint.

## TypeScript

```ts
import { UbagClient } from '@ubag/sdk';

const client = new UbagClient({
  gatewayUrl: 'http://localhost:8081',
  appSecret: process.env.UBAG_APP_SECRET,
  apiVersion: '2026-05-22',
});

const job = await client.jobs.create({
  target: 'https://chat.openai.com',
  commandType: 'send_message',
  input: { prompt: 'Hello, world!' },
});

console.log('Job ID:', job.id);
console.log('Status:', job.status);
```

## Go

```go
package main

import (
    "context"
    "fmt"
    "os"
    "log"
    ubag "github.com/ubag/ubag-go"
)

func main() {
    client := ubag.NewClient(ubag.Config{
        GatewayURL: "http://localhost:8081",
        AppSecret:  os.Getenv("UBAG_APP_SECRET"),
    })

    job, err := client.Jobs.Create(context.Background(), ubag.CreateJobInput{
        Target:      "https://chat.openai.com",
        CommandType: "send_message",
        Input:       map[string]any{"prompt": "Hello, world!"},
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Job ID:", job.ID)
}
```

## curl

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "job": {
      "target": "https://chat.openai.com",
      "command_type": "send_message",
      "input": { "prompt": "Hello, world!" }
    },
    "client": {
      "app_id": "my-app",
      "app_version": "1.0.0",
      "sdk": { "name": "curl", "version": "0.0.0" }
    }
  }'
```

## Related

- [Stream Results](/cookbook/02-stream-results) — follow job progress via SSE
- [Cancel a Job](/cookbook/13-cancel-job) — stop a running job
- [Retry a Job](/cookbook/14-retry-job) — resubmit a failed job
