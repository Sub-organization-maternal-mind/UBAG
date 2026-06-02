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
| POST | /v1/jobs/{id}:cancel | Cancel a job |
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

All errors follow RFC 7807 Problem Details:

```json
{
  "type": "https://ubag.io/errors/job-not-found",
  "title": "Job not found",
  "status": 404,
  "detail": "No job with ID abc-123 exists in this tenant",
  "instance": "/v1/jobs/abc-123",
  "request_id": "req_01HZ..."
}
```

See [Error Catalog](/contracts/error-catalog) for the full list of error types.
