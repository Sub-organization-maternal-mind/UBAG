#!/usr/bin/env bash
# Reset / rotate the UBAG dashboard operator Basic Auth password.
#
# WHY THIS EXISTS
#   The dashboard + API (ubag.polytronx.com/dashboard/, /v1/*) are gated by the
#   nginx-dashboard container's HTTP Basic Auth, using the .htpasswd file next to
#   this script (mounted read-only into the container at /etc/nginx/.htpasswd).
#   The password is stored ONLY as a one-way hash and is NOT kept in env.local,
#   so it cannot be recovered. A secret rotation that regenerates .htpasswd will
#   silently lock out the operator (browser re-prompts forever; nginx logs
#   `user "operator": password mismatch`) until it is re-set with this script.
#
# USAGE
#   ./set-operator-password.sh                  # generate a strong random password (printed once)
#   ./set-operator-password.sh 'my-password'    # set a specific password
#   USER_NAME=operator ./set-operator-password.sh   # override username (default: operator)
#
# After running, verify against the origin (bypasses Cloudflare):
#   curl -s -o /dev/null -w '%{http_code}\n' -u operator:PASSWORD http://127.0.0.1:8083/dashboard/   # expect 200
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HTPASSWD="${SCRIPT_DIR}/.htpasswd"
USER_NAME="${USER_NAME:-operator}"
CONTAINER="${NGINX_CONTAINER:-ubag-small-nginx-dashboard-1}"

command -v openssl >/dev/null 2>&1 || { echo "error: openssl is required but not found" >&2; exit 1; }

PW="${1:-}"
GENERATED=0
if [ -z "$PW" ]; then
  # 20 unambiguous alphanumerics — strong, and easy to type into a browser dialog.
  PW="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
  GENERATED=1
fi

# apr1 (Apache MD5) — portable via openssl, matches the existing hash format.
HASH="$(openssl passwd -apr1 "$PW")"

# Back up any existing file, then write an LF-only htpasswd (CRLF breaks nginx auth).
if [ -f "$HTPASSWD" ]; then
  cp -a "$HTPASSWD" "${HTPASSWD}.bak.$(date +%s)"
fi
printf '%s:%s\n' "$USER_NAME" "$HASH" > "$HTPASSWD"
echo "wrote ${HTPASSWD} (user: ${USER_NAME})"

# Hot-reload nginx if the container is up; otherwise it picks the change up on next start.
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  if docker exec "$CONTAINER" nginx -t >/dev/null 2>&1; then
    docker exec "$CONTAINER" nginx -s reload && echo "reloaded nginx in ${CONTAINER}"
  else
    echo "warning: nginx -t failed in ${CONTAINER}; not reloading" >&2
  fi
else
  echo "note: ${CONTAINER} not running — restart nginx-dashboard to apply" >&2
fi

if [ "$GENERATED" = 1 ]; then
  echo
  echo "NEW ${USER_NAME} password: ${PW}"
  echo "Store it in your password manager — it cannot be recovered from the hash."
fi
