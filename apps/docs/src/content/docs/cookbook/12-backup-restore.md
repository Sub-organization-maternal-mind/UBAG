---
title: Back Up and Restore
description: How to back up the UBAG gateway state and restore it after a failure or migration.
---

UBAG state spans Postgres (jobs, audit log, configs) and Garage S3 (artifacts).
Both must be backed up consistently.

## Full backup

```bash
# Postgres logical backup
pg_dump $UBAG_POSTGRES_URL | gzip > ubag-$(date +%Y%m%d).sql.gz

# Artifact store (Garage S3)
aws s3 sync s3://ubag-artifacts/ ./backup/artifacts/ \
  --endpoint-url http://localhost:3900
```

## Restore Postgres

```bash
gunzip -c ubag-20260522.sql.gz | psql $UBAG_POSTGRES_URL_RESTORE
```

## Restore artifacts

```bash
aws s3 sync ./backup/artifacts/ s3://ubag-artifacts/ \
  --endpoint-url http://localhost:3900
```

## Automated backup via CLI

```bash
ubag-cli backup \
  --postgres-url $UBAG_POSTGRES_URL \
  --artifact-bucket ubag-artifacts \
  --garage-endpoint http://localhost:3900 \
  --output s3://my-backups/ubag/
```

## Verify backup integrity

```bash
ubag-cli backup verify --file s3://my-backups/ubag/ubag-20260522.tar.gz
# Checking Postgres dump... OK
# Checking artifact manifest... OK (12,483 objects)
# Audit log chain... OK (hash chain intact)
```

## Disaster recovery RTO/RPO

| Tier | RPO | RTO |
|------|-----|-----|
| Small | 24h (daily backup) | 2h |
| Standard | 1h (WAL streaming) | 30min |
| Enterprise | ~0 (pgactive sync) | <5min |

## Verify gateway is healthy after restore

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     http://localhost:8081/v1/health
```

See [Disaster Recovery](/operations/disaster-recovery) for the full runbook.
