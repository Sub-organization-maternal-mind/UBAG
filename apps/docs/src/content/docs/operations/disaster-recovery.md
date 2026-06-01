---
title: Disaster Recovery
description: Disaster recovery procedures for UBAG — RPO 5m / RTO 30m targets with failover playbooks for all components.
---

# Disaster Recovery

## Recovery Targets

| Target | Value | Notes |
|--------|-------|-------|
| RPO (Recovery Point Objective) | **5 minutes** | Maximum data loss. Achieved via WAL archiving every 5 minutes. |
| RTO (Recovery Time Objective) | **30 minutes** | Maximum downtime. Covers restore + restart + verification. |

## Components

| Component | Failure Mode | Recovery Path |
|-----------|-------------|---------------|
| PostgreSQL | Data loss / corruption | Restore from WAL archive (see below) |
| MinIO | Object loss | Re-sync from replica or reupload artifacts |
| NATS JetStream | Stream loss | Re-create streams; jobs replay from Postgres |
| Gateway | Process crash | Restart (stateless); state is in Postgres |
| Worker | Process crash | Restart; in-flight jobs requeued automatically |
| Edge SQLite | Corruption | Restore from `ubag backup` snapshot |

## Backup and Restore Commands

### Create a backup

```bash
# SQLite (edge/small profile)
ubag backup --out /backups/ubag-$(date +%Y%m%d)

# With S3/MinIO destination
ubag backup --out s3://ubag-artifacts/backups/$(date +%Y%m%d)
```

### Restore from backup

```bash
# From local backup
ubag restore --from /backups/ubag-20260101

# From S3/MinIO
ubag restore --from s3://ubag-artifacts/backups/20260101
```

### Apply migrations (idempotent)

```bash
# SQLite
ubag migrate --store sqlite

# PostgreSQL
ubag migrate --store postgres --dsn "$UBAG_POSTGRES_DSN"
```

## PostgreSQL — Restore from WAL Archive

**Trigger:** Data loss, corruption, or a failed migration that needs rollback.

**Prerequisites:** WAL archives in MinIO at `ubag-artifacts/wal-archive/`.

### Steps

1. Stop the gateway to prevent new writes:
   ```bash
   docker compose -f docker-compose.small.yml stop gateway
   ```

2. List available WAL archives:
   ```bash
   mc ls minio/ubag-artifacts/wal-archive/ | sort | tail -20
   ```

3. Identify the target point-in-time (the last archive before the incident).

4. Stop Postgres:
   ```bash
   docker compose -f docker-compose.small.yml stop postgres
   ```

5. Extract the WAL archive:
   ```bash
   mc cp minio/ubag-artifacts/wal-archive/<TIMESTAMP>.tar.gz /tmp/
   mkdir -p /tmp/pgdata
   tar -xzf /tmp/<TIMESTAMP>.tar.gz -C /tmp/pgdata/
   ```

6. Replace the Postgres data directory:
   ```bash
   docker compose -f docker-compose.small.yml run --rm postgres \
     bash -c "rm -rf /var/lib/postgresql/data/* && cp -r /tmp/pgdata/* /var/lib/postgresql/data/"
   ```

7. Start Postgres and verify:
   ```bash
   docker compose -f docker-compose.small.yml start postgres
   docker compose -f docker-compose.small.yml exec postgres \
     psql -U ubag -c "SELECT count(*) FROM jobs; SELECT now();"
   ```

8. Run migrations to ensure schema is current:
   ```bash
   ubag migrate --store postgres
   ```

9. Restart the gateway:
   ```bash
   docker compose -f docker-compose.small.yml start gateway
   curl -s http://localhost:8080/v1/ready
   ```

**Expected RTO:** 20–25 minutes.

## MinIO — Re-sync Artifacts

**Trigger:** MinIO data loss or volume corruption.

### Steps

1. Re-create the MinIO bucket if needed:
   ```bash
   mc mb minio/ubag-artifacts
   mc anonymous set none minio/ubag-artifacts
   ```

2. Re-sync from a replica (if configured) or accept the data loss for artifact content (artifacts are cache-able from upstream).

3. Verify gateway can write artifacts:
   ```bash
   curl -s http://localhost:8080/v1/ready
   ```

**Note:** Artifact loss does not affect job records (stored in Postgres). Jobs can be re-run.

## NATS JetStream — Stream Recovery

**Trigger:** NATS volume loss or stream corruption.

### Steps

1. Stop the gateway:
   ```bash
   docker compose -f docker-compose.small.yml stop gateway
   ```

2. Re-create NATS streams (they are auto-created on gateway startup by the NATS executor).

3. Start the gateway — it will re-create any missing streams:
   ```bash
   docker compose -f docker-compose.small.yml start gateway
   ```

4. Re-queue any jobs stuck in `queued` status:
   ```bash
   # Jobs with status=queued but no NATS message can be found via:
   ubag jobs list --status queued
   # Re-submit them if needed
   ```

**Note:** Jobs already in Postgres are not lost. Only the in-flight NATS messages are lost.

## Edge SQLite — Restore

**Trigger:** Edge SQLite DB corruption (WAL mode issue, disk full, unclean shutdown).

### Steps

1. Stop the edge gateway.

2. Restore from the last SQLite backup:
   ```bash
   ubag restore --from /backups/ubag-last-good
   ```

3. Verify:
   ```bash
   sqlite3 ubag-gateway.db "PRAGMA integrity_check;"
   ```

4. Restart the edge gateway.

## Alert Playbooks

### `gateway_slo_error_budget_burn_rate_high`

**Severity:** Warning / Critical

**Meaning:** The error budget for gateway SLOs is burning faster than expected.

**Steps:**
1. Check `/v1/ready` on the gateway — is it returning 200?
2. Review the Grafana dashboard for error rate by endpoint.
3. Check for circuit breakers open: look for `UBAG-QUEUE-BREAKER-OPEN-001` errors in logs.
4. Check NATS stream depth — is the queue backing up?
5. If the issue is a dead worker: restart the worker service.

### `ubag_job_failure_rate_high`

**Severity:** Warning

**Meaning:** Job failure rate exceeds threshold.

**Steps:**
1. Check recent job errors: `ubag jobs list --status failed --limit 20`
2. Check worker logs for adapter errors.
3. If failures are from one target: check if that target's circuit breaker is open.
4. If widespread: check Postgres connectivity and NATS health.

### `ubag_webhook_delivery_failure`

**Severity:** Warning

**Meaning:** Webhook deliveries are failing.

**Steps:**
1. Check webhook endpoint health from gateway logs.
2. Look for `circuit_open` errors — the webhook circuit breaker may be open for that endpoint.
3. Verify `UBAG_WEBHOOK_WORKER_ENABLED=true` in the gateway environment.
4. Check webhook secret configuration.

### `postgres_connection_pool_exhausted`

**Severity:** Critical

**Meaning:** Postgres connection pool is full; new requests are queuing.

**Steps:**
1. Check `pg_stat_activity` for idle connections.
2. Restart the gateway to reset connection pools.
3. Increase `UBAG_POSTGRES_MAX_CONNECTIONS` if needed.

## DR Drill Checklist

Run this drill monthly to verify the RTO/RPO targets are achievable:

- [ ] Take a backup: `ubag backup --out /tmp/drill-backup`
- [ ] Verify the backup: check `manifest.json` exists and `ubag restore --from /tmp/drill-backup --dry-run`
- [ ] Drop the test DB: (use a non-production environment)
- [ ] Restore: `ubag restore --from /tmp/drill-backup`
- [ ] Verify: `curl http://localhost:8080/v1/ready` returns 200
- [ ] Verify a prior job is retrievable: `ubag jobs get <job-id>`
- [ ] Record elapsed time — must be < 30 minutes total
