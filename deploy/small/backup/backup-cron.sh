#!/bin/sh
# Full database backup script — runs pg_dump and uploads to MinIO.
# Scheduled every hour via the backup-cron service.
set -eu

TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
DUMP_FILE="/tmp/ubag-backup-${TIMESTAMP}.pgdump"

pg_dump \
  --host="${POSTGRES_HOST}" \
  --port=5432 \
  --username="${POSTGRES_USER}" \
  --dbname="${POSTGRES_DB}" \
  --format=custom \
  --no-password \
  --file="${DUMP_FILE}" 2>&1 || { echo "pg_dump failed"; exit 1; }

OBJECT_KEY="backups/${TIMESTAMP}.pgdump"
curl -s -X PUT \
  -H "Content-Type: application/octet-stream" \
  --upload-file "${DUMP_FILE}" \
  "http://${MINIO_ENDPOINT}/${MINIO_BUCKET}/${OBJECT_KEY}" \
  -u "${MINIO_ACCESS_KEY}:${MINIO_SECRET_KEY}" || echo "Upload failed"

rm -f "${DUMP_FILE}"
echo "Backup ${TIMESTAMP} completed → ${OBJECT_KEY}"
