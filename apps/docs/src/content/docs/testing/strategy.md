---
title: Testing Strategy
description: Test layers across docs, contracts, services, workers, SDKs, and operations.
---

## Milestone 0

- Docs build.
- Navigation coverage.
- Blueprint coverage check.
- Hallmark responsive checks for docs site.

## v0

- Gateway unit tests.
- Queue/storage conformance tests for edge profile.
- Worker mock target tests.
- Adapter contract tests.
- SDK smoke conformance for TypeScript and Go.

### v0 foundation command surface

The v0 worker slice exposes executable root commands for schemas, edge storage,
security, worker, SDK, sidecar, conformance, observability, CLI, dashboard,
deployment, docs, gateway, and full-chain gates:

```powershell
cmd /c pnpm test:schema
cmd /c pnpm test:edge-store
cmd /c pnpm test:security
cmd /c pnpm test:worker
cmd /c pnpm test:sidecar
cmd /c pnpm test:sdk
cmd /c pnpm test:conformance
cmd /c pnpm test:observability
cmd /c pnpm test:cli
cmd /c pnpm test:dashboard
cmd /c pnpm test:deployment
cmd /c pnpm test:docs
cmd /c pnpm test:gateway
cmd /c pnpm test:v0
cmd /c pnpm check
```

`test:schema` validates the documented schema, queue, migration, job, and error-contract anchors. `test:docs` runs the Starlight build and responsive docs gate. `test:worker` runs the Python safe-mode worker and adapter tests. `test:sdk` checks generated contract freshness and the TypeScript and Go SDK runners. `test:gateway` runs Go gateway tests with memory stores by default.

### Optional Postgres integration tests

The default gateway suite does not require live Postgres; Postgres integration
tests skip unless `UBAG_TEST_POSTGRES_DSN` is set. Use a disposable database
because the tests apply `migrations/postgres/0001_gateway_stores.sql` before
exercising the Postgres job/event and idempotency stores.

```powershell
cmd /c pnpm test:gateway

$env:UBAG_TEST_POSTGRES_DSN="postgres://ubag:password@127.0.0.1:5432/ubag_test?sslmode=disable"
cmd /c pnpm test:gateway
Remove-Item Env:\UBAG_TEST_POSTGRES_DSN
```

## v1

- Integration tests with gateway, worker, DB, object storage, mock target.
- Load tests for non-browser path.
- Chaos tests for worker crash, queue delay, DB outage, and webhook failure.
- Security tests for auth scopes, audit, secrets, rate limits, and webhooks.

## v2

- Multi-region failover tests.
- Installer and Helm/Terraform smoke tests.
- Plugin sandbox tests.
- Broader TypeScript and Go conformance for any newly claimed transports.
