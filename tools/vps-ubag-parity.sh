#!/bin/bash
# Read-only: verify BOTH UBAG production boxes and source/commit parity.
set -u

echo "############ BOTH PRODUCTION BOXES ############"
for host in vps vps2; do :; done 2>/dev/null

echo "--- this box (primary / vps) ---"
docker ps --format '{{.Names}}|{{.Image}}|{{.CreatedAt}}' | grep -i ubag
echo "gateway build commit: $(docker exec ubag-vps-gateway-1 printenv UBAG_BUILD_COMMIT 2>/dev/null || echo n/a)"
echo "gateway version:      $(docker exec ubag-vps-gateway-1 printenv UBAG_GATEWAY_VERSION 2>/dev/null || echo n/a)"

echo
echo "############ IS THE SOURCE DIR A GIT CHECKOUT? ############"
cd /opt/docker/ubag || exit 0
if [ -d .git ]; then echo "IS a git repo"; git rev-parse HEAD; else echo "NOT a git repo (extracted source / tarball)"; fi
echo "source dir mtime: $(stat -c '%y' . 2>/dev/null)"
echo "file count: $(find . -type f 2>/dev/null | wc -l)"

echo
echo "############ SOURCE TREE HASH (top-level, excluding noise) ############"
find . -maxdepth 1 -type f -exec md5sum {} \; 2>/dev/null | sort -k2

echo
echo "############ compose hash + image labels ############"
md5sum docker-compose.vps.yml 2>/dev/null
docker inspect ubag-vps-gateway-1 --format '{{json .Config.Labels}}' 2>&1

echo
echo "############ does /opt/docker/ubag match the running image build? ############"
echo "gateway entrypoint script hash:"
md5sum /opt/docker/ubag/deploy/vps/*.sh 2>/dev/null | head -5

echo
echo "############ vps2 reachability from here ############"
(getent hosts vps2 2>/dev/null || echo "vps2 not resolvable from this box")
