# UBAG OpenAPI Contract

`openapi.yaml` is the OpenAPI 3.1 REST contract for the UBAG gateway surface. It currently covers the v0 `/v1` operational routes, collection routes, job lifecycle, webhook replay, SSE, and WebSocket upgrade entrypoint.

Key route families:

- Health, readiness, version, and metrics.
- Jobs create/list/get/events/cancel/retry.
- SSE job events and WebSocket stream upgrade.
- Workflows, templates, targets, adapters, apps, devices, webhooks, cache, and audit collections.
- Webhook replay.

The API document references shared JSON Schema Draft 2020-12 files from `../shared-schemas/schemas`.

This package contains public contract artifacts only. Gateway runtime code lives under `apps/gateway`.
