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
job is rejected. The actual upload MIME type must exactly match the manifest.
The hard manifest ceiling is 32 files. The currently exposed ChatGPT, Gemini,
and DeepSeek targets allow 10 files, 32 MiB per file, and a derived 320 MiB total.
Content-type support remains provider-specific and is returned by
`GET /v1/adapters`: DeepSeek Web currently accepts documents and images only.
Its live composer removes file upload in Expert mode, so UBAG selects Instant
for DeepSeek attachment jobs. Audio, voice, and video are rejected at job
creation rather than being silently dropped by DeepSeek.

## Flow A — key-reference (two-step)

Create the job with an `attachments` manifest, then upload each file. The job is
held (`status: created`) and dispatches automatically once every declared key has
been uploaded.

```ts
import { UbagClient } from '@ubag/sdk';
import { readFile } from 'node:fs/promises';

const client = new UbagClient({ baseUrl, appSecret });
const pdf = await readFile('report.pdf');

const job = await client.createJobWithAttachments(
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

`submitJobWithAttachments` remains available as a compatible alias. The Go SDK
uses `CreateJobWithAttachments` (with `SubmitJobWithAttachments` retained).

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
  --prompt "Summarize these" \
  --attach report.pdf \
  --attach voice-note.webm:voice
```

Repeat `--attach <path>[:kind]` once per file. Kind defaults from the extension;
the optional final suffix is one of `document | image | audio | video | voice`.
A Windows drive-letter colon is preserved because only a final valid kind suffix
is parsed. Unknown extensions and duplicate basenames are rejected before send.

## Notes

- **Dispatch gate:** a key-reference job is never enqueued until all declared keys
  are present, so a worker never leases a job whose bytes are missing. A job that
  never receives its uploads is failed after a TTL.
- **Idempotency:** both flows require an `Idempotency-Key`; the SDK helpers
  generate one when omitted.
- **Warm browser:** explicitly setting `UBAG_WORKER_DAEMON=true` keeps a
  provider page hot between jobs; attachment materialization runs identically under
  the daemon, and each reused page clears pending file state so files never leak
  between jobs. The source/Compose default is `false`; the bundled script is
  `/app/apps/worker/run_worker_daemon.py`.
- **Safe mode:** attachments are user-supplied files into user-owned sessions. Do
  not place credentials, tokens, or session material in attachment metadata — the
  gateway rejects secret-like values in `job.input`.
