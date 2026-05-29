---
title: Runtime Recovery Runbook
description: First operator playbooks for UBAG alerts and recovery.
---

## Adapter drift

Open adapter details, compare last successful and failed DOM snapshots, patch selectors, run adapter tests, canary the new version, and promote or rollback.

## Queue backlog

Check queue depth, worker capacity, target rate limits, tenant caps, and autoscaling status. Apply backpressure before accepting unbounded work.

Observable signals: inspect `ubag_queue_depth{state="queued"}`, `ubag_queue_oldest_job_age_seconds`, and the `ubag_worker_jobs_processed_total` rate. In NATS mode, also check the NATS monitor endpoint at `:8222/healthz`; in file-spool mode, compare pending and leased file counts under `UBAG_EXECUTOR_SPOOL_DIR`.

## Gateway dispatch failure

If `/v1/ready` reports `UBAG-QUEUE-EXECUTOR-READY-001` or `/v1/metrics` shows queue depth stuck without worker progress, verify executor mode, spool directory permissions, disk space, and whether the worker consumer is intentionally disabled. In `noop` mode, accepted jobs remain gateway-visible but are not executed by a worker.

Observable signals: check `ubag_worker_result_ingestions_total{outcome="failure"}`, `ubag_worker_result_ingestion_duration_seconds`, and gateway logs for queue lease, poison, or terminal notification failures.

For local file-spool mode, inspect `pending/`, `leased/`, `done/`, `failed/`, and `cancelled/` under `UBAG_EXECUTOR_SPOOL_DIR`. A growing `leased/` directory usually means the embedded consumer or Python worker crashed mid-run. A growing `failed/` directory means the worker returned non-terminal output or execution failed; use gateway job events rather than raw worker stderr for client-facing diagnosis. Cancelled jobs must remain terminal and should not be re-enqueued.

For NATS mode, verify `UBAG_EXECUTOR_MODE=nats`, `UBAG_NATS_URL`, `UBAG_NATS_STREAM`, and `UBAG_NATS_SUBJECT`. In the small profile, `UBAG_EXECUTOR_MODE=nats` requires `.\deploy\small\small.ps1 -Action up -Profile queue`. Confirm the NATS service is reachable and JetStream is enabled. If `UBAG_WORKER_CONSUMER_ENABLED=true`, also verify `UBAG_NATS_WORKER_DURABLE`, `UBAG_NATS_WORKER_ACK_WAIT_MS`, `UBAG_NATS_WORKER_NAK_DELAY_MS`, `UBAG_NATS_WORKER_FETCH_WAIT_MS`, and `UBAG_NATS_WORKER_MAX_DELIVER`; poison messages usually indicate malformed envelopes or subject/header mismatches, while repeated delayed redelivery points to transient gateway store readiness.

## Artifact storage failure

If `/v1/ready` reports `UBAG-QUEUE-ARTIFACT-READY-001`, verify `UBAG_ARTIFACT_STORE`, MinIO endpoint, access key, secret key, bucket name, and TLS flag. In Postgres-backed metadata mode, also verify that `migrations/postgres/0002_artifact_metadata.sql` has been applied and that `/v1/ready` can see the `artifact_metadata` table.

Observable signals: confirm the MinIO live endpoint `/minio/health/live`, verify new rows in `artifact_metadata`, and compare object presence in the configured bucket if metadata/blob consistency is suspected.

For shared or production-like deployments, configure bucket lifecycle retention,
server-side encryption, backup/restore coverage, and malware/content scanning
appropriate to the data class before enabling long-lived artifacts.

## Webhook delivery failure

If `/v1/ready` reports `UBAG-QUEUE-WEBHOOK-READY-001`, verify
`UBAG_WEBHOOK_OUTBOX`, `UBAG_GATEWAY_STORE`, `UBAG_POSTGRES_DSN`, and that
`migrations/postgres/0003_webhook_outbox.sql` has been applied. If delivery
depth grows in `/v1/metrics`, check `ubag_webhook_outbox_depth` and
`ubag_webhook_outbox_oldest_age_seconds` by state, confirm
`UBAG_WEBHOOK_WORKER_ENABLED=true`, verify the webhook signing secret, and
inspect the endpoint's 4xx/5xx behavior. Replay only an existing delivery ID
with an audit reason and idempotency key.

When the worker is enabled in the small profile, keep
`UBAG_WEBHOOK_ALLOWED_HOSTS` populated unless an explicit outbound SSRF review
approves `UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true`. URL validation and
connect-time delivery checks reject private, local, link-local, and metadata
addresses.

## Browser session quarantined

Review quarantine reason. If login or CAPTCHA blocks progress, use manual live session takeover with short-lived token and audit reason.

## Worker crash

Verify unacknowledged jobs requeued, inspect artifacts and logs, check resource limits, and confirm replacement worker health.
