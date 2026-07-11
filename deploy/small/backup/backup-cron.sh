#!/bin/sh
# Full logical backup: pg_dump (custom format) shipped to OFF-HOST S3 storage.
#
# Uploads via the mc client, which signs requests with AWS SigV4. The previous
# version used `curl -X PUT -u key:secret` (HTTP Basic Auth) against MinIO's S3
# endpoint — S3 requires SigV4, so every upload returned 403 and, because `-s`
# and `|| echo` swallowed it, the cron still reported success. It also targeted
# the on-host minio:9000, which violates the no-backups-on-the-VPS rule even
# when auth works. This script refuses to run without an off-host destination.
#
# Fails LOUD: any failed dump/upload exits non-zero. On success it stamps a
# heartbeat file; the service healthcheck flips the container unhealthy when the
# heartbeat goes stale, so a broken backup is visible instead of silent.
set -eu

: "${BACKUP_S3_ENDPOINT:?refusing to run: BACKUP_S3_ENDPOINT is empty. Backups must target an OFF-HOST S3 endpoint (never the on-host minio:9000).}"
: "${BACKUP_S3_ACCESS_KEY:?BACKUP_S3_ACCESS_KEY is required}"
: "${BACKUP_S3_SECRET_KEY:?BACKUP_S3_SECRET_KEY is required}"
: "${BACKUP_S3_BUCKET:?BACKUP_S3_BUCKET is required}"
: "${POSTGRES_HOST:?POSTGRES_HOST is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

if [ "${BACKUP_S3_USE_SSL:-true}" = "true" ]; then SCHEME="https"; else SCHEME="http"; fi
PREFIX="${BACKUP_S3_PREFIX:-ubag-small}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
HEARTBEAT_FILE="${BACKUP_HEARTBEAT_FILE:-/var/lib/backup/last-success-dump}"

TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
DUMP_FILE="/tmp/ubag-backup-${TIMESTAMP}.pgdump"
DEST_DIR="ubagbak/${BACKUP_S3_BUCKET}/${PREFIX}/dumps"
OBJECT="${DEST_DIR}/${TIMESTAMP}.pgdump"

cleanup() { rm -f "${DUMP_FILE}"; }
trap cleanup EXIT

echo "[backup] pg_dump ${POSTGRES_DB}@${POSTGRES_HOST} -> ${SCHEME}://${BACKUP_S3_ENDPOINT}/${BACKUP_S3_BUCKET}/${PREFIX}/dumps/${TIMESTAMP}.pgdump"
pg_dump \
  --host="${POSTGRES_HOST}" \
  --port="${POSTGRES_PORT:-5432}" \
  --username="${POSTGRES_USER}" \
  --dbname="${POSTGRES_DB}" \
  --format=custom \
  --no-password \
  --file="${DUMP_FILE}"

# Register the off-host alias fresh each run (idempotent; picks up rotated creds).
mc alias set ubagbak "${SCHEME}://${BACKUP_S3_ENDPOINT}" \
  "${BACKUP_S3_ACCESS_KEY}" "${BACKUP_S3_SECRET_KEY}" --api S3v4 >/dev/null
# Best-effort bucket create: the off-host bucket is expected to pre-exist, and a
# scoped key may lack CreateBucket rights. Don't let that fail the backup — the
# real operation is the upload below, which surfaces its own errors.
mc mb --ignore-existing "ubagbak/${BACKUP_S3_BUCKET}" >/dev/null 2>&1 || true
mc cp "${DUMP_FILE}" "${OBJECT}"

# Prune old dumps (client-side; portable across MinIO / Garage / managed S3).
if [ "${RETENTION_DAYS}" -gt 0 ] 2>/dev/null; then
  mc rm --recursive --force --older-than "${RETENTION_DAYS}d" "${DEST_DIR}/" \
    || echo "[backup] WARN: retention prune failed (non-fatal; upload succeeded)"
fi

mkdir -p "$(dirname "${HEARTBEAT_FILE}")"
date -u +%s > "${HEARTBEAT_FILE}"
echo "[backup] OK dump ${TIMESTAMP} uploaded off-host (${OBJECT})"
