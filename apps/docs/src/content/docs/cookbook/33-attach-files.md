---
title: Attach Files to a Job
description: Attach documents, images, audio, voice messages, or video to a UBAG job using the key-reference or multipart one-shot flow.
---

UBAG jobs can carry file attachments (documents, images, audio, voice messages,
video) that the worker attaches into the provider's composer before submitting the
prompt. Bytes always travel out-of-band through the job artifact store, never
inline in `job.input`.

Each attachment declares `{ key, content_type, kind }` (plus an optional
`filename`); `kind` is one of `document | image | audio | video | voice`. The
target adapter must declare an `attachments` policy (see
[List Adapters](/cookbook/18-list-adapters/)) that accepts the content type, or the
job is rejected. The per-file limit is 32 MiB; the adapter policy sets the maximum
count.

## Flow A — key-reference (two-step)

Create the job with an `attachments` manifest, then upload each file. The job is
held (`status: created`) and dispatches automatically once every declared key has
been uploaded.

```ts
import { UbagClient } from '@ubag/sdk';
import { readFile } from 'node:fs/promises';

const client = new UbagClient({ baseUrl, appSecret });
const pdf = await readFile('report.pdf');

const job = await client.submitJobWithAttachments(
  {
    client: { app_id: 'my-app', app_version: '1.0.0' },
    job: {
      target: 'chatgpt_web',
      command_type: 'chat.prompt',
      input: { prompt: 'Summarize the attached report.' }
    }
  },
  [{ key: 'report.pdf', content_type: 'application/pdf', kind: 'document', body: pdf }]
);
// job.status === 'created' until the upload lands, then the gateway dispatches it.
```

The Go SDK mirrors this with `SubmitJobWithAttachments`.

## Flow B — multipart one-shot

Send the job envelope and all files in a single `multipart/form-data` request. The
first part must be the `job` JSON envelope; each remaining part is a file whose
field name equals its attachment `key`. The job is born complete and dispatches
immediately.

```ts
const job = await client.createJobMultipart(
  {
    client: { app_id: 'my-app', app_version: '1.0.0' },
    job: {
      target: 'chatgpt_web',
      command_type: 'chat.prompt',
      input: { prompt: 'Transcribe and summarize.' }
    }
  },
  [
    { key: 'note.webm', content_type: 'audio/webm', kind: 'voice', body: voiceBytes },
    { key: 'chart.png', content_type: 'image/png', kind: 'image', body: chartBytes }
  ]
);
```

### CLI

```bash
ubag create-job --target chatgpt_web --command-type chat.prompt \
  --prompt "Summarize these" --attach report.pdf,notes.md
```

`--attach` takes a comma-separated list of file paths and uses the multipart
one-shot flow, inferring each file's content type and kind from its extension.

## Notes

- **Dispatch gate:** a key-reference job is never enqueued until all declared keys
  are present, so a worker never leases a job whose bytes are missing. A job that
  never receives its uploads is failed after a TTL.
- **Idempotency:** both flows require an `Idempotency-Key`; the SDK helpers
  generate one when omitted.
- **Warm browser:** enabling the warm-browser daemon (`UBAG_WORKER_DAEMON`) keeps a
  provider page hot between jobs; attachment materialization runs identically under
  the daemon, and each reused page is reloaded so files never leak between jobs.
- **Safe mode:** attachments are user-supplied files into user-owned sessions. Do
  not place credentials, tokens, or session material in attachment metadata — the
  gateway rejects secret-like values in `job.input`.
