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

# --- Live-provider job dispatch ---
# Route jobs through the embedded worker consumer to the live Playwright engine,
# which attaches over CDP to the operator's logged-in Chrome (the live-browser
# bridge on 58091) and drives chatgpt_web/deepseek_web/gemini_web/etc. Mock jobs
# still work (run_live_worker.py routes target=mock to the mock adapter).
mkdir -p spool
: "${UBAG_EXECUTOR_MODE:=file}"
: "${UBAG_EXECUTOR_SPOOL_DIR:=$PWD/spool}"
: "${UBAG_WORKER_CONSUMER_ENABLED:=true}"
# Real interpreter: bare "python" resolves to a broken Windows Store alias here.
: "${UBAG_WORKER_PYTHON:=C:/Users/Admin/AppData/Local/Python/bin/python.exe}"
[ -x "$UBAG_WORKER_PYTHON" ] || UBAG_WORKER_PYTHON=python
: "${UBAG_WORKER_SCRIPT:=$PWD/../worker/run_live_worker.py}"
# Live provider responses (esp. reasoning models) can take a while.
: "${UBAG_WORKER_MAX_RUNTIME_MS:=180000}"
# The shared logged-in Chrome the worker attaches to (the live-browser bridge).
: "${UBAG_REMOTE_BROWSER_ENDPOINT:=http://127.0.0.1:58091}"
# open() requires a non-empty profile dir even on the CDP-attach path.
: "${UBAG_PROFILE_DIR:=$PWD/../../tools/live-browser/chrome-profile}"

export UBAG_GATEWAY_ADDR UBAG_APP_SECRET UBAG_APP_ID UBAG_ACTOR_ROLE UBAG_CONVERSATIONS_ENABLED \
  UBAG_DEV_CORS_ORIGIN UBAG_GATEWAY_STORE UBAG_ARTIFACT_STORE UBAG_ARTIFACT_DIR \
  UBAG_EXECUTOR_MODE UBAG_EXECUTOR_SPOOL_DIR UBAG_WORKER_CONSUMER_ENABLED \
  UBAG_WORKER_PYTHON UBAG_WORKER_SCRIPT UBAG_WORKER_MAX_RUNTIME_MS \
  UBAG_REMOTE_BROWSER_ENDPOINT UBAG_PROFILE_DIR

if [ ! -f ./ubag-gateway.exe ] && [ ! -f ./ubag-gateway ]; then
  echo "Building gateway binary..."
  go build -o ubag-gateway.exe ./cmd/gateway
fi

BIN=./ubag-gateway.exe
[ -f "$BIN" ] || BIN=./ubag-gateway

echo "Starting gateway on ${UBAG_GATEWAY_ADDR} (store=${UBAG_GATEWAY_STORE}, artifacts=${UBAG_ARTIFACT_STORE}:${UBAG_ARTIFACT_DIR})"
exec "$BIN"
