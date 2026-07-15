#!/usr/bin/env bash
# Forced-command entrypoint for the GitHub Actions deploy key.
#
# The CI key in ~/.ssh/authorized_keys is pinned to this script via command=,
# so a leaked key cannot open a shell or run anything else on the box -- it can
# only deploy the gateway to an image tag this script itself validates.
#
# Contract (caller side):
#   ssh -i <key> root@host deploy-gateway sha-<commit>   <<< "<ghcr-token>"
#
# The GHCR token arrives on stdin, is used for one pull, and is logged out
# immediately. It is GitHub Actions' ephemeral GITHUB_TOKEN (dies with the job),
# so no long-lived registry credential is ever written to this host.
set -euo pipefail

readonly REPO_DIR=/opt/ubag
readonly COMPOSE_FILE=docker-compose.small.yml
readonly ENV_FILE=deploy/small/env.local
readonly REGISTRY=ghcr.io
readonly IMAGE_REPO=ghcr.io/sub-organization-maternal-mind/ubag-gateway

log() { printf '[ci-deploy] %s\n' "$*"; }
fail() { printf '[ci-deploy] REFUSED: %s\n' "$*" >&2; exit 1; }

# --- validate the request -----------------------------------------------------
# Parse SSH_ORIGINAL_COMMAND rather than trusting the caller's argv: with a
# forced command, argv is this script's own, and the caller's request lands here.
read -r action tag _extra <<<"${SSH_ORIGINAL_COMMAND:-}"

[ "${action:-}" = "deploy-gateway" ] || fail "only 'deploy-gateway' is permitted (got '${action:-}')"
[ -z "${_extra:-}" ] || fail "unexpected extra arguments"

# Only immutable sha-<40 hex> tags. Refusing moving tags (:latest, :branch) means
# a deploy always names one exact build that can be traced back to a commit, and
# closes off tag-injection via SSH_ORIGINAL_COMMAND.
[[ "${tag:-}" =~ ^sha-[0-9a-f]{40}$ ]] || fail "tag must be sha-<40-hex-commit>, got '${tag:-}'"

readonly IMAGE="${IMAGE_REPO}:${tag}"
cd "$REPO_DIR"

# --- authenticate, pull, deploy ----------------------------------------------
token="$(cat)"
[ -n "$token" ] || fail "no registry token on stdin"

cleanup() { docker logout "$REGISTRY" >/dev/null 2>&1 || true; }
trap cleanup EXIT

printf '%s' "$token" | docker login "$REGISTRY" -u ubag-ci --password-stdin >/dev/null
unset token
log "authenticated to $REGISTRY"

# Retry the pull: this is a multi-hundred-MB transfer over the public internet,
# and a reset partway through is transient rather than fatal. Docker keeps the
# layers it already fetched, so each attempt resumes rather than restarts.
# (A GitHub-CDN IPv6 PMTU blackhole caused exactly this; /etc/gai.conf now
# de-prioritizes 2606:50c0::/32, and this retry covers the residual flakiness.)
log "pulling $IMAGE"
pulled=0
for attempt in 1 2 3; do
  if docker pull -q "$IMAGE" >/dev/null; then
    pulled=1
    log "pull ok on attempt ${attempt}"
    break
  fi
  log "pull attempt ${attempt} failed; retrying in $((attempt * 5))s"
  sleep $((attempt * 5))
done
[ "$pulled" = 1 ] || fail "could not pull $IMAGE after 3 attempts"

# Pin the image for this and every future `up`. env.local is gitignored and
# VPS-only, so this is the one place the running tag is recorded.
previous="$(grep -E '^UBAG_GATEWAY_IMAGE=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
cp "$ENV_FILE" "${ENV_FILE}.ci-deploy.bak"
sed -i '/^UBAG_GATEWAY_IMAGE=/d' "$ENV_FILE"
printf 'UBAG_GATEWAY_IMAGE=%s\n' "$IMAGE" >>"$ENV_FILE"
log "pinned UBAG_GATEWAY_IMAGE=$IMAGE (was: ${previous:-<unset>})"

log "recreating gateway"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps --force-recreate gateway

# --- verify -------------------------------------------------------------------
# Health is checked through nginx -> gateway, the same docker-network path
# RadioPad uses, so a pass means RadioPad's real route works.
log "waiting for gateway health"
for i in $(seq 1 30); do
  if docker exec ubag-small-nginx-dashboard-1 wget -qO- -T5 http://gateway:8080/v1/ready >/dev/null 2>&1; then
    log "gateway healthy after ${i} attempt(s)"
    log "running image: $(docker inspect ubag-small-gateway-1 --format '{{.Config.Image}}')"
    exit 0
  fi
  sleep 2
done

# Roll back to the previously pinned image rather than leaving prod down: this
# drives RadioPad's report generation, so a failed deploy must not linger.
log "gateway did NOT become healthy; rolling back"
cp "${ENV_FILE}.ci-deploy.bak" "$ENV_FILE"
docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps --force-recreate gateway || true
fail "health check failed after 60s; rolled back to ${previous:-<unset>}"
