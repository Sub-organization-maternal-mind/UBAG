---
title: Job Contract
description: Request and response envelope requirements.
---

## Request envelope

Every job request carries:

- `api_version`
- `idempotency_key`
- `client` metadata: app, version, device, user reference, SDK name and version.
- `job` metadata: target, command type, conversation, template, input, options, callbacks, context.

## Response envelope

Every job response carries:

- `api_version`
- `job_id`
- `idempotent_replay`
- `status`
- `target`
- `result`
- `metadata`
- `trace_id`
- `events_url`

## Output normalization

The result model supports text, Markdown, plain text, sections, and HTML. The local file-spool consumer normalizes mock worker `result` events into `output.text` and `output.plain_text`, records validation metadata, and stores the result on the public job response. v1 adds adapter-specific schema validation, retry-with-critique for malformed outputs, and renderers for HTML, DOCX, and PDF.

## Contract artifacts

Initial REST and JSON Schema contract artifacts now live in:

- `packages/openapi/openapi.yaml`
- `packages/shared-schemas/schemas/job-request.schema.json`
- `packages/shared-schemas/schemas/job-response.schema.json`
- `packages/shared-schemas/schemas/error.schema.json`
- `packages/shared-schemas/schemas/job-event.schema.json`

These files are executable contract sources for the gateway, SDKs, mock worker, conformance runners, and documentation checks.
