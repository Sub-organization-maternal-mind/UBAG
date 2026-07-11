#!/bin/sh
# Periodic Postgres BASE BACKUP shipped to OFF-HOST S3 storage.
#
# IMPORTANT — despite the historical name, this ships a full `pg_basebackup`
# snapshot on each run; it does NOT continuously stream WAL segments. It yields
# coarse, snapshot-based recovery points at the configured interval, not true
# log-shipping PITR. See operations/disaster-recovery.md ("True WAL archiving")
# for the follow-up that wires archive_command for continuous WAL. Because each
# run copies the whole cluster, keep the interval sane on a busy DB.
#
# Uploads via mc (SigV4), off-host only. The previous version used curl Basic
# Auth against the on-host minio:9000 — invalid for S3 and on-host, so it
# silently failed. Fails loud; a heartbeat file surfaces failures to the
# service healthcheck.
set -eu

: "${BACKUP_S3_ENDPOINT:?refusing to run: BACKUP_S3_ENDPOINT is empty. Backups must target an OFF-HOST S3 endpoint (never the on-host minio:9000).}"
: "${BACKUP_S3_ACCESS_KEY:?BACKUP_S3_ACCESS_KEY is required}"
: "${BACKUP_S3_SECRET_KEY:?BACKUP_S3_SECRET_KEY is required}"
: "${BACKUP_S3_BUCKET:?BACKUP_S3_BUCKET is required}"
: "${POSTGRES_HOST:?POSTGRES_HOST is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"

if [ "${BACKUP_S3_USE_SSL:-true}" = "true" ]; then SCHEME="https"; else SCHEME="http"; fi
PREFIX="${BACKUP_S3_PREFIX:-ubag-small}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
HEARTBEAT_FILE="${BACKUP_HEARTBEAT_FILE:-/var/lib/backup/last-success-base}"

TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
STAGE_DIR="/var/lib/wal-archive/${TIMESTAMP}"
TARBALL="/tmp/base-${TIMESTAMP}.tar.gz"
DEST_DIR="ubagbak/${BACKUP_S3_BUCKET}/${PREFIX}/base"
OBJECT="${DEST_DIR}/${TIMESTAMP}.tar.gz"

cleanup() { rm -rf "${STAGE_DIR}" "${TARBALL}"; }
trap cleanup EXIT

mkdir -p "${STAGE_DIR}"
echo "[wal-archive] pg_basebackup ${POSTGRES_HOST} -> ${SCHEME}://${BACKUP_S3_ENDPOINT}/${BACKUP_S3_BUCKET}/${PREFIX}/base/${TIMESTAMP}.tar.gz"
pg_basebackup \
  --host="${POSTGRES_HOST}" \
  --port="${POSTGRES_PORT:-5432}" \
  --username="${POSTGRES_USER}" \
  --pgdata="${STAGE_DIR}" \
  --wal-method=stream \
  --checkpoint=fast \
  --format=plain \
  --no-password \
  --progress
tar -czf "${TARBALL}" -C "${STAGE_DIR}" .

# Register the off-host alias fresh each run (idempotent; picks up rotated creds).
mc alias set ubagbak "${SCHEME}://${BACKUP_S3_ENDPOINT}" \
  "${BACKUP_S3_ACCESS_KEY}" "${BACKUP_S3_SECRET_KEY}" --api S3v4 >/dev/null
# Best-effort bucket create (see backup-cron.sh) — don't fail the backup if a
# scoped key can't create an already-existing off-host bucket.
mc mb --ignore-existing "ubagbak/${BACKUP_S3_BUCKET}" >/dev/null 2>&1 || true
mc cp "${TARBALL}" "${OBJECT}"

# Prune old base backups (client-side; portable across MinIO / Garage / managed S3).
if [ "${RETENTION_DAYS}" -gt 0 ] 2>/dev/null; then
  mc rm --recursive --force --older-than "${RETENTION_DAYS}d" "${DEST_DIR}/" \
    || echo "[wal-archive] WARN: retention prune failed (non-fatal; upload succeeded)"
fi

mkdir -p "$(dirname "${HEARTBEAT_FILE}")"
date -u +%s > "${HEARTBEAT_FILE}"
echo "[wal-archive] OK base backup ${TIMESTAMP} uploaded off-host (${OBJECT})"
