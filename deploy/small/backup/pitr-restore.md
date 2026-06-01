# PITR Restore Procedure

## Prerequisites
- MinIO running with WAL archives at `ubag-artifacts/wal-archive/`
- Postgres stopped or recoverable

## Steps

### 1. Stop the gateway
```
docker compose -f docker-compose.small.yml stop gateway
```

### 2. Identify the target restore point
List WAL archives in MinIO:
```
mc ls minio/ubag-artifacts/wal-archive/
```

### 3. Download and extract the WAL archive
```
mc cp minio/ubag-artifacts/wal-archive/<timestamp>.tar.gz /tmp/restore/
tar -xzf /tmp/restore/<timestamp>.tar.gz -C /tmp/restore/pgdata/
```

### 4. Replace the Postgres data directory
```
docker compose -f docker-compose.small.yml stop postgres
docker compose -f docker-compose.small.yml run --rm postgres \
  bash -c "rm -rf /var/lib/postgresql/data/* && cp -r /tmp/pgdata/* /var/lib/postgresql/data/"
```

### 5. Start Postgres and verify
```
docker compose -f docker-compose.small.yml start postgres
docker compose -f docker-compose.small.yml exec postgres psql -U ubag -c "SELECT count(*) FROM jobs"
```

### 6. Restart the gateway
```
docker compose -f docker-compose.small.yml start gateway
```

## RTO / RPO
- **RPO**: 5 minutes (WAL archive interval)
- **RTO**: 30 minutes (manual restore following this guide)
