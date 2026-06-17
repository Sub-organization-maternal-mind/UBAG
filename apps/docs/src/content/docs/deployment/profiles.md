---
title: Deployment Profiles
description: Edge, small, standard, and enterprise deployment targets.
---

## Edge

Single-machine profile for developer laptops, Raspberry Pi, NUC, or a small VPS. The gateway can run with in-memory defaults or SQLite/localfs persistence through `UBAG_GATEWAY_STORE=sqlite` and `UBAG_ARTIFACT_STORE=localfs`.

## Small

Docker Compose profile with gateway, worker, Postgres, MinIO, nginx-dashboard ingress, Grafana, Prometheus, Dragonfly/Valkey, and optional NATS.

The first small-profile scaffold lives in `docker-compose.small.yml` with service
configuration under `deploy/small`. The gateway defaults to in-memory job and
idempotency stores, and `UBAG_GATEWAY_STORE=postgres` plus `UBAG_POSTGRES_DSN`
enables Postgres-backed jobs, job events, worker-event dedupe keys, and
idempotency records after `migrations/postgres/0001_gateway_stores.sql` is
applied. `migrations/postgres/0002_artifact_metadata.sql` adds gateway-owned
artifact metadata for MinIO mode. Executor dispatch defaults to `noop`; local
file-spool dispatch and result ingestion can be enabled with
`UBAG_EXECUTOR_MODE=file`, `UBAG_EXECUTOR_SPOOL_DIR`, and
`UBAG_WORKER_CONSUMER_ENABLED=true`. `UBAG_EXECUTOR_MODE=nats` publishes
accepted jobs to NATS JetStream using `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and
`UBAG_NATS_SUBJECT`. `UBAG_ARTIFACT_STORE=minio` stores job artifacts through
MinIO using `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`,
`UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL`.
`migrations/postgres/0003_webhook_outbox.sql` adds durable signed webhook
deliveries, and `UBAG_WEBHOOK_WORKER_ENABLED=true` runs the retry worker when
`UBAG_WEBHOOK_OUTBOX=postgres`, `UBAG_WEBHOOK_SECRET`, and callback URL policy
settings are configured. The small profile also includes a rerunnable Postgres
`migrate` action for existing volumes, a `minio-init` bootstrap that creates the
artifact bucket and least-privilege gateway policy, separate MinIO root and
gateway credentials, and a local nginx-dashboard ingress that can sit behind a
public TLS reverse proxy.
See [Small Compose Profile](/deployment/small-profile/) for local startup
commands and the current wiring boundary.

## Standard

Kubernetes profile with Postgres, NATS JetStream, object storage, observability, autoscaled workers, and production ingress.

## Enterprise

Multi-region profile with tenant residency, SSO/SAML/SCIM, mTLS mesh, SIEM export, geo-replicated storage, and disaster recovery.
