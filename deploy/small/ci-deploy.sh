#!/usr/bin/env bash
# Forced-command entrypoint for the GitHub Actions deploy key.
#
# The CI key in ~/.ssh/authorized_keys is pinned to this script via command=,
# so a leaked key cannot open a shell or run anything else on the box -- it can
# only deploy the gateway to a validated image tag, or replace the dashboard
# static bundle. Nothing here builds anything: the GitHub Actions runners do
# the compiling and the VPS only pulls/unpacks.
#
# Contract (caller side):
#   ssh -i <key> root@host deploy-gateway sha-<commit>   <<< "<ghcr-token>"
#   ssh -i <key> root@host deploy-dashboard              <  dashboard-dist.tar.gz
#
# The GHCR token arrives on stdin, is used for one pull, and is logged out
# immediately. It is GitHub Actions' ephemeral GITHUB_TOKEN (dies with the job),
# so no long-lived registry credential is ever written to this host. The
# dashboard tarball (~1MB) is streamed on stdin and unpacked with an atomic
# swap so a truncated upload can never leave a half-written dist behind.
set -euo pipefail

readonly REPO_DIR=/opt/docker/ubag
readonly COMPOSE_FILE=docker-compose.vps.yml
readonly ENV_FILE=deploy/vps/env.local
readonly REGISTRY=ghcr.io
readonly IMAGE_REPO=ghcr.io/sub-organization-maternal-mind/ubag-gateway
readonly BROWSER_IMAGE_REPO=ghcr.io/sub-organization-maternal-mind/ubag-browser
readonly DASHBOARD_DIR="$REPO_DIR/apps/dashboard/dist"

log() { printf '[ci-deploy] %s\n' "$*"; }
fail() { printf '[ci-deploy] REFUSED: %s\n' "$*" >&2; exit 1; }

deploy_gateway() {
  local tag="$1"
  local IMAGE="${IMAGE_REPO}:${tag}"
  local BROWSER_IMAGE="${BROWSER_IMAGE_REPO}:${tag}"
  cd "$REPO_DIR"

  # --- authenticate, pull, deploy --------------------------------------------
  local token
  token="$(cat)"
  [ -n "$token" ] || fail "no registry token on stdin"

  local cleanup trap_prev config_container="" config_dir=""
  trap_prev="$(trap -p EXIT)"
  cleanup() {
    docker logout "$REGISTRY" >/dev/null 2>&1 || true
    [ -z "$config_container" ] || docker rm "$config_container" >/dev/null 2>&1 || true
    [ -z "$config_dir" ] || rm -rf "$config_dir"
  }
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
  local pulled=0 attempt
  for attempt in 1 2 3; do
    if docker pull -q "$IMAGE" >/dev/null && docker pull -q "$BROWSER_IMAGE" >/dev/null; then
      pulled=1
      log "pull ok on attempt ${attempt}"
      break
    fi
    log "pull attempt ${attempt} failed; retrying in $((attempt * 5))s"
    sleep $((attempt * 5))
  done
  [ "$pulled" = 1 ] || fail "could not pull $IMAGE after 3 attempts"

  # Extract and validate this revision's runtime configuration without starting
  # the image. Preserve one generation alongside env.local for rollback.
  config_dir="$(mktemp -d /tmp/ubag-deploy-config.XXXXXX)"
  config_container="$(docker create "$IMAGE")"
  docker cp "$config_container:/app/deploy-config/." "$config_dir/"
  docker compose --project-directory "$REPO_DIR" -f "$config_dir/docker-compose.vps.yml" --env-file "$ENV_FILE" config -q
  cp "$COMPOSE_FILE" "${COMPOSE_FILE}.ci-deploy.bak"
  cp deploy/small/nginx-dashboard/default.conf.template deploy/small/nginx-dashboard/default.conf.template.ci-deploy.bak
  cp "$config_dir/docker-compose.vps.yml" "$COMPOSE_FILE"
  cp "$config_dir/default.conf.template" deploy/small/nginx-dashboard/default.conf.template

  # Pin the image for this and every future `up`. env.local is gitignored and
  # VPS-only, so this is the one place the running tag is recorded.
  local previous
  previous="$(grep -E '^UBAG_GATEWAY_IMAGE=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
  cp "$ENV_FILE" "${ENV_FILE}.ci-deploy.bak"
  sed -i '/^UBAG_GATEWAY_IMAGE=/d' "$ENV_FILE"
  printf 'UBAG_GATEWAY_IMAGE=%s\n' "$IMAGE" >>"$ENV_FILE"
  sed -i '/^UBAG_BROWSER_IMAGE=/d' "$ENV_FILE"
  printf 'UBAG_BROWSER_IMAGE=%s\n' "$BROWSER_IMAGE" >>"$ENV_FILE"
  log "pinned UBAG_GATEWAY_IMAGE=$IMAGE (was: ${previous:-<unset>})"

  # Record the commit this image was built from. The box is a tarball extract,
  # not a git checkout, so env.local is the only place the running build's
  # provenance is kept; /v1/ready and version consumers read this value.
  local commit previous_commit
  commit="${tag#sha-}"
  previous_commit="$(grep -E '^UBAG_BUILD_COMMIT=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)"
  sed -i '/^UBAG_BUILD_COMMIT=/d' "$ENV_FILE"
  printf 'UBAG_BUILD_COMMIT=%s\n' "$commit" >>"$ENV_FILE"
  log "pinned UBAG_BUILD_COMMIT=$commit (was: ${previous_commit:-<unset>})"

  log "recreating browser, gateway, and chat reaper"
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps --force-recreate browser gateway chat-reaper

  # --- verify -------------------------------------------------------------------
  # Health is checked through nginx -> gateway, the same docker-network path
  # RadioPad uses, so a pass means RadioPad's real route works.
  log "waiting for gateway health"
  local i
  for i in $(seq 1 30); do
    if docker exec ubag-nginx-dashboard wget -qO- -T5 http://gateway:8080/v1/ready >/dev/null 2>&1 \
      && [ "$(docker inspect ubag-vps-browser --format '{{.State.Health.Status}}')" = healthy ]; then
      log "gateway healthy after ${i} attempt(s)"
      log "running image: $(docker inspect ubag-vps-gateway-1 --format '{{.Config.Image}}')"
      exit 0
    fi
    sleep 2
  done

  # Roll back to the previously pinned image rather than leaving prod down: this
  # drives RadioPad's report generation, so a failed deploy must not linger.
  log "gateway did NOT become healthy; rolling back"
  cp "${ENV_FILE}.ci-deploy.bak" "$ENV_FILE"
  cp "${COMPOSE_FILE}.ci-deploy.bak" "$COMPOSE_FILE"
  cp deploy/small/nginx-dashboard/default.conf.template.ci-deploy.bak deploy/small/nginx-dashboard/default.conf.template
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps --force-recreate browser gateway chat-reaper || true
  fail "health check failed after 60s; rolled back to ${previous:-<unset>}"
}

deploy_dashboard() {
  cd "$REPO_DIR"

  local staging="${DASHBOARD_DIR}.incoming.$$"
  local previous_dir="${DASHBOARD_DIR}.previous"
  mkdir -p "$staging"

  # Unpack to a staging dir first; a truncated/corrupt tarball must never take
  # over the live document root. tar exits non-zero on a short read, which with
  # set -e aborts before the swap. The CI tarball is created with
  # `tar -czf ... -C apps/dashboard dist`, i.e. everything under a top-level
  # dist/ component — strip it so staging IS the document root.
  if ! tar -xzf - -C "$staging" --strip-components=1; then
    rm -rf "$staging"
    fail "dashboard tarball unpack failed; live dist untouched"
  fi
  [ -f "$staging/index.html" ] || { rm -rf "$staging"; fail "tarball has no index.html; live dist untouched"; }

  # Atomic-ish swap: keep the current dist one generation back for instant
  # rollback. The host path swap is not enough on its own — the nginx container
  # bind-mounts this directory and the mount pins the directory INODE, so it
  # must be recreated to re-bind to the swapped-in directory. nginx:alpine
  # starts in about a second.
  rm -rf "$previous_dir"
  if [ -d "$DASHBOARD_DIR" ]; then mv "$DASHBOARD_DIR" "$previous_dir"; fi
  mv "$staging" "$DASHBOARD_DIR"
  log "dashboard dist replaced ($(du -sh "$DASHBOARD_DIR" | cut -f1), was $(du -sh "$previous_dir" 2>/dev/null | cut -f1 || echo '?')); recreating nginx-dashboard to re-bind the mount"
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps --force-recreate nginx-dashboard >/dev/null
  sleep 3

  # Verify through the same nginx the operator uses. /dashboard/ itself is
  # behind basic auth, so probe the public no-auth service worker instead.
  # On failure, roll back.
  local i
  for i in $(seq 1 15); do
    if docker exec ubag-nginx-dashboard wget -qO- -T5 http://127.0.0.1/dashboard/sw.js >/dev/null 2>&1; then
      log "dashboard healthy after ${i} attempt(s)"
      return 0
    fi
    sleep 2
  done
  log "dashboard did NOT come up; rolling back"
  rm -rf "$DASHBOARD_DIR"
  [ -d "$previous_dir" ] && mv "$previous_dir" "$DASHBOARD_DIR"
  fail "dashboard health check failed; rolled back to previous dist"
}

# --- validate the request -----------------------------------------------------
# Parse SSH_ORIGINAL_COMMAND rather than trusting the caller's argv: with a
# forced command, argv is this script's own, and the caller's request lands here.
read -r action tag _extra <<<"${SSH_ORIGINAL_COMMAND:-}"

case "${action:-}" in
  deploy-gateway)
    [ -z "${_extra:-}" ] || fail "unexpected extra arguments"
    # Only immutable sha-<40 hex> tags. Refusing moving tags (:latest, :branch)
    # means a deploy always names one exact build that can be traced back to a
    # commit, and closes off tag-injection via SSH_ORIGINAL_COMMAND.
    [[ "${tag:-}" =~ ^sha-[0-9a-f]{40}$ ]] || fail "tag must be sha-<40-hex-commit>, got '${tag:-}'"
    deploy_gateway "$tag"
    ;;
  deploy-dashboard)
    [ -z "${tag:-}" ] && [ -z "${_extra:-}" ] || fail "deploy-dashboard takes no arguments"
    deploy_dashboard
    ;;
  *)
    fail "only 'deploy-gateway sha-<commit>' or 'deploy-dashboard' are permitted (got '${action:-}')"
    ;;
esac
