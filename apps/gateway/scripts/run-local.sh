#!/usr/bin/env bash
# Runs the gateway locally with persistent storage (SQLite + localfs
# artifacts) so job, idempotency, and conversation data survives process
# restarts and laptop reboots. Not part of the build/test pipeline — a
# convenience script for local development only.
#
# Usage: bash apps/gateway/scripts/run-local.sh
# Data lives in apps/gateway/ubag-gateway.db* and apps/gateway/artifacts/ —
# both are gitignored. Delete them to start from a clean slate.

set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p artifacts

: "${UBAG_GATEWAY_ADDR:=:58080}"
: "${UBAG_APP_SECRET:=dev_local_secret_12345678}"
: "${UBAG_APP_ID:=dev-app}"
# Empty UBAG_ACTOR_ROLE normalizes to "service" (job:* actions only), which
# denies Browser Sessions, Webhooks, Audit, Users & Roles, etc. This is a
# single-user local dev gateway with no real tenant boundary to protect, so
# grant full access rather than hitting the same 403 on every other page.
: "${UBAG_ACTOR_ROLE:=superadmin}"
: "${UBAG_CONVERSATIONS_ENABLED:=true}"
: "${UBAG_DEV_CORS_ORIGIN:=http://localhost:58179}"
: "${UBAG_GATEWAY_STORE:=sqlite}"
: "${UBAG_ARTIFACT_STORE:=localfs}"
: "${UBAG_ARTIFACT_DIR:=./artifacts}"

export UBAG_GATEWAY_ADDR UBAG_APP_SECRET UBAG_APP_ID UBAG_ACTOR_ROLE UBAG_CONVERSATIONS_ENABLED \
  UBAG_DEV_CORS_ORIGIN UBAG_GATEWAY_STORE UBAG_ARTIFACT_STORE UBAG_ARTIFACT_DIR

if [ ! -f ./ubag-gateway.exe ] && [ ! -f ./ubag-gateway ]; then
  echo "Building gateway binary..."
  go build -o ubag-gateway.exe ./cmd/gateway
fi

BIN=./ubag-gateway.exe
[ -f "$BIN" ] || BIN=./ubag-gateway

echo "Starting gateway on ${UBAG_GATEWAY_ADDR} (store=${UBAG_GATEWAY_STORE}, artifacts=${UBAG_ARTIFACT_STORE}:${UBAG_ARTIFACT_DIR})"
exec "$BIN"
