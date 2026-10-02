#!/bin/bash
# Read-only: establish the exact UBAG production build identity.
set -u

echo "############ PRODUCTION UBAG CONTAINERS ############"
docker ps --format '{{.Names}}|{{.Image}}|{{.Status}}|{{.CreatedAt}}' | grep ubag

echo
echo "############ IMAGE IDENTITY ############"
for c in ubag-vps-gateway-1 ubag-vps-browser ubag-vps-chat-reaper; do
  echo "--- $c ---"
  docker inspect "$c" --format 'image_id={{.Image}}
created={{.Created}}
compose_config={{index .Config.Labels "com.docker.compose.project.config_files"}}
compose_project={{index .Config.Labels "com.docker.compose.project"}}' 2>&1
  echo "build_commit_env=$(docker exec "$c" printenv UBAG_BUILD_COMMIT 2>/dev/null || echo n/a)"
  echo "version_env=$(docker exec "$c" printenv UBAG_GATEWAY_VERSION 2>/dev/null || echo n/a)"
done

echo
echo "############ IMAGE BUILD LABELS (from docker image inspect) ############"
for img in $(docker ps --format '{{.Image}}' | grep -i ubag | sort -u); do
  echo "--- image: $img ---"
  docker image inspect "$img" --format '{{json .Config.Labels}}' 2>&1 | head -c 800
  echo
  docker image inspect "$img" --format 'created={{.Created}}' 2>&1
done

echo
echo "############ SOURCE DIR: /opt/docker/ubag ############"
cd /opt/docker/ubag || exit 0
echo "git? -> $(git rev-parse --short HEAD 2>&1 | head -1)"
echo "files newer than 2026-09-13 (non-.git):"
find . -path ./.git -prune -o -type f -newermt "2026-09-13" -print 2>/dev/null | grep -v node_modules | head -20

echo
echo "############ COMPOSE FILE MTIME + HASH ############"
for f in docker-compose.vps.yml docker-compose.small.yml; do
  [ -f "$f" ] && md5sum "$f" && stat -c '%y %n' "$f"
done
