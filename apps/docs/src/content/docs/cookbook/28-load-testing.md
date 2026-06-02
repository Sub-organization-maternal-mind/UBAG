---
title: Load Test the Gateway
description: Run load tests against the UBAG gateway using k6 or the built-in load test runner.
---

Use k6 to simulate concurrent job submissions and measure gateway throughput and latency.

## k6 script

```js
// ubag-load-test.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomUUID } from 'k6/experimental/webcrypto';

export const options = {
  vus: 20,          // 20 concurrent virtual users
  duration: '60s',
};

const GATEWAY = __ENV.UBAG_GATEWAY || 'http://localhost:8081';
const TOKEN   = __ENV.UBAG_APP_SECRET;

export default function () {
  const res = http.post(
    `${GATEWAY}/v1/jobs`,
    JSON.stringify({
      job: { target: 'https://example.com', command_type: 'health_check' },
    }),
    {
      headers: {
        'Authorization': `Bearer ${TOKEN}`,
        'Ubag-Api-Version': '2026-05-22',
        'Content-Type': 'application/json',
        'Idempotency-Key': randomUUID(),
      },
    }
  );

  check(res, {
    'status is 201': r => r.status === 201,
    'has job id':    r => JSON.parse(r.body).id !== undefined,
  });
  sleep(0.5);
}
```

## Run

```bash
UBAG_GATEWAY=http://localhost:8081 \
UBAG_APP_SECRET=$UBAG_APP_SECRET \
k6 run ubag-load-test.js
```

## Built-in load runner

```bash
ubag-cli bench \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET \
  --concurrency 20 \
  --duration 60s \
  --command health_check
```

## Observe during the test

```bash
# In a separate terminal — watch Prometheus metrics
curl http://localhost:8081/v1/metrics | grep ubag_job
```

## Targets to watch

| Metric | Healthy range |
|--------|--------------|
| `ubag_job_queue_depth` | < 50 sustained |
| `ubag_job_p99_latency_ms` | < 5000 |
| `ubag_gateway_error_rate` | < 1% |

See [Observability](/operations/observability) for the full metrics reference.
