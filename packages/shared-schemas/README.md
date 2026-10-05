# UBAG Shared Schemas

This package contains JSON Schema Draft 2020-12 contracts shared by the REST API, SDK generators, mock gateway, and future adapter tests.

The `schemas/` directory holds the initial REST/OpenAPI-facing v1 baseline schemas. Their `$id` values are namespaced under `https://schemas.ubag.dev/v1/rest/`.

## v1 baseline

- `schemas/job-request.schema.json`
- `schemas/job-response.schema.json`
- `schemas/error.schema.json`
- `schemas/job-event.schema.json`

## Worker daemon protocol (internal, `$id` under `/v1/worker/`)

- `schemas/worker-daemon-request.schema.json`: stdin line (v1 job line, opt-in v2 `proto`/`attempt`/`slot_id`, hello/probe control lines)
- `schemas/worker-daemon-job-end.schema.json`: stdout JOB_END marker (v1 fields unchanged, opt-in v2 `attempt_id`/`slot_id`/`pid`/`warm_key`/`outcome_signal`/`submitted`) and the control reply

Shared vectors: `packages/conformance/fixtures/worker-daemon/v2.json`.

These files define public wire contracts only. They do not contain product runtime code.
