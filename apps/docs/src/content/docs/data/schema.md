---
title: Schema
description: Logical database entities and dialect strategy.
---

## Core entities

Tenants, projects, apps, credentials, devices, targets, adapters, app-target permissions, prompt templates, jobs, job events, webhooks, webhook deliveries, browser sessions, semantic cache, audit log, outbox events, blob objects, dead-letter jobs, idempotency keys, queue messages, schema migrations, and deployment profile snapshots.

## Postgres

Postgres uses JSONB, arrays, TIMESTAMPTZ, partitioned job/event tables, pgvector HNSW indexes, pg_partman, and pg_cron where available.

The small-profile gateway store currently ships `migrations/postgres/0001_gateway_stores.sql`, `migrations/postgres/0002_artifact_metadata.sql`, and `migrations/postgres/0003_webhook_outbox.sql` for the runtime gateway slice:

- `gateway_jobs` for accepted jobs, lifecycle status, result envelopes, trace IDs, and request payload maps.
- `gateway_job_events` for gateway-sequenced job and worker-event history.
- `gateway_job_worker_event_keys` for idempotent worker event ingestion.
- `gateway_idempotency_records` for mutating-route replay/conflict decisions.
- `artifact_metadata` for gateway-owned MinIO/S3 artifact metadata, including artifact key, internal object key, content type, byte size, SHA-256 checksum, and creation time.
- `gateway_webhook_deliveries` for signed webhook outbox entries, delivery state, retry leasing, replay linkage, and tenant/app scoping.
- `gateway_webhook_attempts` for bounded delivery attempt history, HTTP status, error class, redacted error message, duration, and attempt number.

Set `UBAG_GATEWAY_STORE=postgres` with `UBAG_POSTGRES_DSN` to activate these gateway stores. `/v1/ready` checks that the configured database is reachable and the required gateway tables exist. When `UBAG_ARTIFACT_STORE=minio` uses Postgres metadata, readiness also verifies `artifact_metadata`. When `UBAG_WEBHOOK_OUTBOX=postgres`, readiness verifies `gateway_webhook_deliveries` and `gateway_webhook_attempts`.

## SQLite

SQLite uses JSON text, RFC3339 timestamps, integer primary keys, no pgvector, no partitioning, and periodic compaction or archive tables.

## Migration policy

Migrations are forward-only and use expand, migrate, contract sequencing. Rollback is restore-from-backup.
