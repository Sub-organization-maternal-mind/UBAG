# UBAG Shared Schemas

This package contains JSON Schema Draft 2020-12 contracts shared by the REST API, SDK generators, mock gateway, and future adapter tests.

The `schemas/` directory holds the initial REST/OpenAPI-facing v1 baseline schemas. Their `$id` values are namespaced under `https://schemas.ubag.dev/v1/rest/`.

## v1 baseline

- `schemas/job-request.schema.json`
- `schemas/job-response.schema.json`
- `schemas/error.schema.json`
- `schemas/job-event.schema.json`

These files define public wire contracts only. They do not contain product runtime code.
