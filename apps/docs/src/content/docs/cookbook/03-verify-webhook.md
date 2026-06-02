---
title: Verify a Webhook Signature
description: Validate UBAG webhook payloads using HMAC-SHA256 signature verification.
---

UBAG signs every webhook delivery with `HMAC-SHA256` over the raw request body.
Verify the signature before processing the payload.

## TypeScript (Express)

```ts
import crypto from 'node:crypto';
import express from 'express';

const app = express();
app.use(express.raw({ type: 'application/json' }));

app.post('/webhooks/ubag', (req, res) => {
  const sig = req.headers['ubag-signature'] as string;
  const secret = process.env.UBAG_WEBHOOK_SECRET!;

  const expected = crypto
    .createHmac('sha256', secret)
    .update(req.body)
    .digest('hex');

  if (!crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(`sha256=${expected}`))) {
    return res.status(401).send('Invalid signature');
  }

  const payload = JSON.parse(req.body.toString());
  console.log('Event:', payload.type, payload.job_id);
  res.status(200).send('ok');
});
```

## Python (FastAPI)

```python
import hashlib, hmac, os
from fastapi import FastAPI, Request, HTTPException

app = FastAPI()
SECRET = os.environ["UBAG_WEBHOOK_SECRET"].encode()

@app.post("/webhooks/ubag")
async def handle(request: Request):
    body = await request.body()
    sig = request.headers.get("ubag-signature", "")
    expected = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(sig, expected):
        raise HTTPException(status_code=401, detail="Invalid signature")
    payload = await request.json()
    print("Event:", payload["type"])
    return {"ok": True}
```

## Signature format

```
ubag-signature: sha256=<hex-digest>
```

The digest is computed over the raw, unmodified request body bytes.
Always use `timingSafeEqual` / `compare_digest` to prevent timing attacks.

## Test a delivery

Send a test webhook delivery from the gateway:

```bash
curl -X POST http://localhost:8081/v1/webhooks/$WEBHOOK_ID/test \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22"
```

See [Webhooks](/contracts/webhooks) for the full payload schema and event types.
See [Manage Webhooks](/cookbook/21-manage-webhooks) for creating webhook endpoints.
