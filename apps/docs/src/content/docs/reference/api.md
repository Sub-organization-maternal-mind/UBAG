---
title: API Reference
description: UBAG Gateway REST API — generated from packages/openapi/openapi.yaml
---

The full UBAG Gateway REST API reference is available in machine-readable OpenAPI 3.1 format at `packages/openapi/openapi.yaml`.

## Spec location

| File | Description |
|------|-------------|
| `packages/openapi/openapi.yaml` | OpenAPI 3.1 spec — source of truth |
| `packages/proto/` | protobuf definitions (SSE stream contracts) |

## Key endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /v1/health | Health check |
| POST | /v1/jobs | Create a job |
| GET | /v1/jobs/{id} | Get job by ID |
| POST | /v1/jobs/{id}/cancel | Cancel a job |
| POST | /v1/jobs/{id}/retry | Retry a failed job |
| GET | /v1/targets | List targets |
| GET | /v1/adapters | List adapters |
| GET | /v1/browser/instances | List browser instances |
| GET | /v1/browser/summary | Browser session summary |
| GET | /v1/metrics | Gateway metrics |
| GET | /v1/audit | Audit log |
| GET | /v1/templates | List templates |
| POST | /v1/templates/{id}/render | Render a template |
| GET | /v1/workflows | List workflows |
| GET | /v1/webhooks | List webhook endpoints |
| POST | /v1/openai/chat/completions | OpenAI-compatible chat completion (sync bridge over one native job) |
| GET | /v1/openai/models | List facade model IDs (`target` and `target\|setting`) |
| POST | /v1/openai/audio/transcriptions | Transcribe audio via a held native job (`{text, ubag_job_id}`) |
| POST | /v1/openai/embeddings | Deterministic hash embeddings in the OpenAI shape (NOT semantic) |

## Required headers

Every request must include:

| Header | Value | Required |
|--------|-------|----------|
| `Authorization` | `Bearer <app_secret>` | Yes (if auth enabled) |
| `Ubag-Api-Version` | `2026-05-22` | Yes |
| `Idempotency-Key` | UUID v4 | Mutations only |

## Interactive exploration

When running the docs dev server locally, an interactive API explorer is available:

```bash
# From apps/docs:
pnpm dev
# Then visit http://localhost:4321/api-reference
```

You can also explore the spec directly with any OpenAPI-compatible tool:

```bash
# Install Scalar CLI
npx @scalar/cli serve packages/openapi/openapi.yaml

# Or use Redoc
npx redoc-cli serve packages/openapi/openapi.yaml
```

## Spec freshness check

The CI pipeline validates that the spec matches the implementation.
If you modify gateway routes, update `packages/openapi/openapi.yaml` and run:

```bash
pnpm check:contracts
```

The `tools/check-api-reference.mjs` script warns if the spec is newer than this reference page.

## Versioning

The API uses date-based versioning. The current stable version is `2026-05-22`.
Pass this in the `Ubag-Api-Version` header on every request. The gateway rejects
requests with missing or unknown version strings.

## Error format

All errors follow the stable UBAG error envelope (`error.schema.json`):

```json
{
  "error": {
    "code": "UBAG-AUTH-001",
    "category": "auth",
    "message": "Missing or invalid bearer credential",
    "retryable": false,
    "doc_url": "https://docs.ubag.dev/contracts/error-catalog",
    "trace_id": "trace_01HZ..."
  }
}
```

See [Error Catalog](/contracts/error-catalog) for the full list of error codes.

See [Error Catalog](/contracts/error-catalog) for the full list of error types.

## OpenAI compatibility facade

`POST /v1/openai/chat/completions` accepts a narrow OpenAI chat-completions
subset and answers with an OpenAI `chat.completion` object, backed by one
native UBAG job plus a terminal wait (see the OpenAPI description for the full
mapping). `GET /v1/openai/models` lists the accepted `model` IDs.
`response_format` serves JSON coercion only (`json_object` / `json_schema`:
the completion text is reduced to its first parseable JSON value, and the
call fails loudly when nothing parses). `ubag_strict` (default off) opts
back into fail-closed picker config: with the default, a drifted provider
model menu skips instead of failing the job — the prompt submits in the
account's current mode. Reusing `ubag_nonce` retries the same native job;
changing it makes an otherwise identical call create a distinct job. File attachments ride
`ubag_attachments` (declare, 202-held, PUT each key, poll/replay).
`POST /v1/openai/audio/transcriptions` takes an OpenAI-shaped multipart
audio upload and answers `{text, ubag_job_id}` once the provider-backed job
is terminal. `POST /v1/openai/embeddings` answers the OpenAI embeddings
shape with deterministic SHA-256-chained unit vectors — correctly shaped and
dimensioned, but NOT semantic (neighbours share bytes, not meaning). Facade
errors are OpenAI-shaped (`error.message/type/code`, with the still-running
job ID in `error.param` on 504); a missing credential keeps the gateway
`UBAG-AUTH-MISSING-001` envelope from shared auth middleware. Usage figures
are character-based estimates, not metered model tokens.
