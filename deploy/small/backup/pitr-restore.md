# Restore Procedure (off-host backups)

The `backup` compose profile ships two artifact types to the **off-host** S3
bucket configured via `UBAG_BACKUP_S3_*` (never the on-host MinIO):

| Prefix | Producer | Format | Restore with |
|--------|----------|--------|--------------|
| `<prefix>/dumps/<ts>.pgdump` | `backup-cron` (hourly) | `pg_dump --format=custom` | `pg_restore` |
| `<prefix>/base/<ts>.tar.gz`  | `postgres-wal-archive` (periodic) | `pg_basebackup` plain tarball | file-copy into the data dir |

`<prefix>` is `UBAG_BACKUP_S3_PREFIX` (default `ubag-small`).

> **Note:** the `base` snapshots are full `pg_basebackup` copies taken on an
> interval — coarse, snapshot-based recovery points, **not** continuous WAL/PITR.
> True log-shipping PITR is a follow-up (see
> `apps/docs/.../operations/disaster-recovery.md` → "True WAL archiving").

## 0. Point mc at the off-host bucket

Run from any host with the backup credentials (e.g. an admin workstation, not
necessarily the VPS):

```sh
SCHEME=$([ "$UBAG_BACKUP_S3_USE_SSL" = "true" ] && echo https || echo http)
mc alias set ubagbak "$SCHEME://$UBAG_BACKUP_S3_ENDPOINT" \
  "$UBAG_BACKUP_S3_ACCESS_KEY" "$UBAG_BACKUP_S3_SECRET_KEY" --api S3v4
mc ls ubagbak/"$UBAG_BACKUP_S3_BUCKET"/ubag-small/dumps/ | sort | tail -20
```

## Option A — restore from an hourly logical dump (recommended)

### 1. Stop the gateway
```sh
docker compose -f docker-compose.small.yml stop gateway
```

### 2. Download the chosen dump
```sh
mc cp ubagbak/"$UBAG_BACKUP_S3_BUCKET"/ubag-small/dumps/<TIMESTAMP>.pgdump /tmp/restore.pgdump
```

### 3. Restore into Postgres
`pg_restore` a custom-format dump into the running postgres container. `--clean
--if-exists` drops and recreates objects; drop `--create` if the database
already exists.
```sh
docker compose -f docker-compose.small.yml cp /tmp/restore.pgdump postgres:/tmp/restore.pgdump
docker compose -f docker-compose.small.yml exec postgres \
  pg_restore --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
             --clean --if-exists --no-owner /tmp/restore.pgdump
```

### 4. Verify and restart
```sh
docker compose -f docker-compose.small.yml exec postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) FROM jobs;"
docker compose -f docker-compose.small.yml start gateway
```

## Option B — restore from a base backup (whole data directory)

### 1. Stop the gateway and Postgres
```sh
docker compose -f docker-compose.small.yml stop gateway postgres
```

### 2. Download and extract the base backup
```sh
mkdir -p /tmp/restore/pgdata
mc cp ubagbak/"$UBAG_BACKUP_S3_BUCKET"/ubag-small/base/<TIMESTAMP>.tar.gz /tmp/restore/base.tar.gz
tar -xzf /tmp/restore/base.tar.gz -C /tmp/restore/pgdata
```

### 3. Replace the Postgres data directory
```sh
docker compose -f docker-compose.small.yml run --rm -v /tmp/restore/pgdata:/restore:ro postgres \
  sh -c "rm -rf /var/lib/postgresql/data/* && cp -a /restore/. /var/lib/postgresql/data/"
```

### 4. Start Postgres and verify
```sh
docker compose -f docker-compose.small.yml start postgres
docker compose -f docker-compose.small.yml exec postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) FROM jobs;"
docker compose -f docker-compose.small.yml start gateway
```

## RTO / RPO
- **RPO**: ~1 hour — hourly logical dumps (`UBAG_BACKUP_FULL_INTERVAL_SECONDS`,
  default 3600s) are the finest-grained recovery points; a full base snapshot is
  taken daily (`UBAG_BACKUP_BASE_INTERVAL_SECONDS`, default 86400s) for
  whole-cluster restore. Snapshot-based — not continuous WAL replay.
- **RTO**: ~30 minutes (manual restore following this guide).
