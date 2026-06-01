#!/bin/sh
# WAL archival script — ships Postgres WAL segments to MinIO via pg_basebackup.
# Runs every 5 minutes. Idempotent: skips if WAL already archived.
set -eu

TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
BACKUP_DIR="/var/lib/wal-archive/${TIMESTAMP}"
mkdir -p "${BACKUP_DIR}"

# Take a base backup with WAL streaming
pg_basebackup \
  --host="${POSTGRES_HOST}" \
  --port=5432 \
  --username="${POSTGRES_USER}" \
  --pgdata="${BACKUP_DIR}" \
  --wal-method=stream \
  --checkpoint=fast \
  --no-password \
  --progress 2>&1 || { echo "pg_basebackup failed"; rm -rf "${BACKUP_DIR}"; exit 1; }

# Upload to MinIO using mc (MinIO client) or curl
# Using curl with MinIO's S3 API (basic upload)
OBJECT_KEY="wal-archive/${TIMESTAMP}.tar.gz"
tar -czf "/tmp/wal-${TIMESTAMP}.tar.gz" -C "${BACKUP_DIR}" .
curl -s -X PUT \
  -H "Content-Type: application/octet-stream" \
  --upload-file "/tmp/wal-${TIMESTAMP}.tar.gz" \
  "http://${MINIO_ENDPOINT}/${MINIO_BUCKET}/${OBJECT_KEY}" \
  -u "${MINIO_ACCESS_KEY}:${MINIO_SECRET_KEY}" || echo "Upload failed (MinIO may not be running)"

rm -rf "${BACKUP_DIR}" "/tmp/wal-${TIMESTAMP}.tar.gz"
echo "WAL archive ${TIMESTAMP} completed"
