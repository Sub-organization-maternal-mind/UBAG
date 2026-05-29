#!/usr/bin/env bash
# UBAG installer (Linux / macOS).
#
# Modes:
#   compose  Run the small-profile Docker Compose stack (default).
#   binary   Install a pre-built ubag-gateway binary to a prefix + (Linux)
#            optionally install a systemd unit.
#
# Safety:
#   - No `curl | bash` of remote code. A binary URL may be provided, but it is
#     downloaded to a temp file and verified against a required SHA-256 checksum
#     before anything is installed or executed.
#   - Idempotent: re-running converges to the same state.
#
# Usage:
#   ./install.sh --mode compose
#   ./install.sh --mode binary --binary ./ubag-gateway --prefix /usr/local
#   ./install.sh --mode binary --url https://host/ubag-gateway \
#       --sha256 <hex> --prefix /usr/local --systemd
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

MODE="compose"
BINARY=""
URL=""
SHA256=""
PREFIX="/usr/local"
INSTALL_SYSTEMD="false"
SERVICE_USER="ubag"
ENV_FILE="/etc/ubag/gateway.env"

log()  { printf '[ubag-install] %s\n' "$*"; }
err()  { printf '[ubag-install] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"; }

usage() {
  sed -n '2,30p' "$0"
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mode)     MODE="${2:?}"; shift 2 ;;
    --binary)   BINARY="${2:?}"; shift 2 ;;
    --url)      URL="${2:?}"; shift 2 ;;
    --sha256)   SHA256="${2:?}"; shift 2 ;;
    --prefix)   PREFIX="${2:?}"; shift 2 ;;
    --systemd)  INSTALL_SYSTEMD="true"; shift ;;
    --user)     SERVICE_USER="${2:?}"; shift 2 ;;
    --env-file) ENV_FILE="${2:?}"; shift 2 ;;
    -h|--help)  usage 0 ;;
    *) err "unknown argument: $1" ;;
  esac
done

verify_sha256() {
  local file="$1" expected="$2" actual
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    err "no sha256sum/shasum available to verify checksum"
  fi
  [ "$actual" = "$expected" ] || err "checksum mismatch: expected $expected got $actual"
  log "checksum verified: $actual"
}

install_compose() {
  need docker
  docker compose version >/dev/null 2>&1 || err "docker compose plugin is required"
  local env_local="${REPO_ROOT}/deploy/small/env.local"
  if [ ! -f "$env_local" ]; then
    log "creating ${env_local} from env.example (replace placeholder secrets before sharing)"
    cp "${REPO_ROOT}/deploy/small/env.example" "$env_local"
  else
    log "reusing existing ${env_local}"
  fi
  if grep -q 'replace-with-local\|set-a-local' "$env_local"; then
    err "placeholder secrets remain in ${env_local}; edit it before starting the stack"
  fi
  log "starting small-profile stack"
  docker compose --env-file "$env_local" \
    -f "${REPO_ROOT}/docker-compose.small.yml" up -d --build
  log "stack started. Gateway health: http://127.0.0.1:8080/v1/health"
}

install_binary() {
  local src tmp=""
  if [ -n "$URL" ]; then
    [ -n "$SHA256" ] || err "--sha256 is required with --url"
    need curl
    tmp="$(mktemp)"
    log "downloading ${URL}"
    curl -fsSL "$URL" -o "$tmp"
    verify_sha256 "$tmp" "$SHA256"
    src="$tmp"
  elif [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || err "binary not found: $BINARY"
    if [ -n "$SHA256" ]; then verify_sha256 "$BINARY" "$SHA256"; fi
    src="$BINARY"
  else
    err "binary mode requires --binary <path> or --url <url> --sha256 <hex>"
  fi

  local sudo=""
  [ "$(id -u)" -eq 0 ] || sudo="sudo"

  log "installing to ${PREFIX}/bin/ubag-gateway"
  $sudo install -d -m 0755 "${PREFIX}/bin"
  $sudo install -m 0755 "$src" "${PREFIX}/bin/ubag-gateway"
  [ -n "$tmp" ] && rm -f "$tmp"

  if [ "$INSTALL_SYSTEMD" = "true" ]; then
    install_systemd "$sudo"
  fi
  log "installed. Run: ${PREFIX}/bin/ubag-gateway (needs UBAG_APP_SECRET in env)"
}

install_systemd() {
  local sudo="$1"
  command -v systemctl >/dev/null 2>&1 || err "systemd not available on this host"
  log "ensuring service user '${SERVICE_USER}'"
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    $sudo useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  fi
  $sudo install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/ubag/executor-spool
  if [ ! -f "$ENV_FILE" ]; then
    log "creating ${ENV_FILE} template (edit before starting; chmod 0600)"
    $sudo install -d -m 0755 "$(dirname "$ENV_FILE")"
    $sudo cp "${SCRIPT_DIR}/gateway.env.example" "$ENV_FILE"
    $sudo chmod 0600 "$ENV_FILE"
  fi
  log "installing systemd unit"
  $sudo install -m 0644 "${SCRIPT_DIR}/systemd/ubag-gateway.service" \
    /etc/systemd/system/ubag-gateway.service
  $sudo sed -i "s#@PREFIX@#${PREFIX}#g; s#@USER@#${SERVICE_USER}#g; s#@ENVFILE@#${ENV_FILE}#g" \
    /etc/systemd/system/ubag-gateway.service
  $sudo systemctl daemon-reload
  log "enable + start with: sudo systemctl enable --now ubag-gateway"
}

case "$MODE" in
  compose) install_compose ;;
  binary)  install_binary ;;
  *) err "unknown --mode: $MODE (expected compose|binary)" ;;
esac
