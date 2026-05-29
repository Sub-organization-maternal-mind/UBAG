---
title: Migrations
description: Schema and profile migration plan.
---

## Schema migrations

Migrations are profile-specific and forward-only. Numbering is monotonic within each profile or dialect, but a SQLite edge migration and a Postgres small-profile migration with the same number are not necessarily the same logical change.

Current Postgres small-profile migrations:

- `0001_gateway_stores.sql`: jobs, job events, worker-event dedupe keys, idempotency records, and `gateway_schema_migrations`.
- `0002_artifact_metadata.sql`: gateway-owned artifact metadata for MinIO/S3 storage and its schema migration ledger row.
- `0003_webhook_outbox.sql`: signed webhook delivery outbox, attempt ledger, replay linkage, and its schema migration ledger row.

Fresh Docker volumes apply these files from `/docker-entrypoint-initdb.d`.
Existing volumes can run the same SQL through
`.\deploy\small\small.ps1 -Action migrate` before enabling the dependent mode.
Rollback is restore-from-backup or disposable volume recreation, not down
migrations.

## Edge to small migration

The future `ubag migrate sqlite-to-postgres` command will:

1. Lock the edge database and checkpoint WAL.
2. Export rows in dependency order.
3. Transform JSON, timestamps, and IDs into Postgres-native types.
4. Copy localfs blobs into MinIO or Garage.
5. Rebuild indexes.
6. Validate counts and checksums.
7. Switch the deployment profile after successful validation.

## Rollback

Rollback is restore-from-backup, not down migrations.

The current repository ships manual SQL migration files and deployment checks;
it does not yet ship the automated edge-to-small migration CLI.
