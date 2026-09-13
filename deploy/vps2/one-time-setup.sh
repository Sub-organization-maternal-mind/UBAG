#!/bin/sh
# UBAG vps2 one-time setup — runs ON 213.163.201.37 as root.
# Inputs in /tmp: ubag-vps2.tar (repo), ubag-dist.tgz (dashboard), ubag-vps2-htpasswd.
# Secrets are generated HERE and never echoed.
set -e

SHA=$(cat /tmp/ubag-vps2-sha)

echo "== 1. extract repo to /opt/docker/ubag"
mkdir -p /opt/docker/ubag
cd /opt/docker/ubag
tar xf /tmp/ubag-vps2.tar

echo "== 2. dashboard dist"
tar xzf /tmp/ubag-dist.tgz -C /opt/docker/ubag/apps/dashboard
test -f /opt/docker/ubag/apps/dashboard/dist/index.html

echo "== 3. operator .htpasswd"
mkdir -p deploy/vps/nginx-dashboard
cp /tmp/ubag-vps2-htpasswd deploy/vps/nginx-dashboard/.htpasswd
chmod 644 deploy/vps/nginx-dashboard/.htpasswd (must be readable by the nginx worker)

echo "== 4. env.local (fresh secrets, generated here)"
if [ -f deploy/vps/env.local ]; then
  echo "   env.local already exists — leaving it untouched"
else
  APP_SECRET=$(openssl rand -hex 32)
  PG_PW=$(openssl rand -hex 24)
  cat > deploy/vps/env.local <<EOF
# UBAG vps2 (213.163.201.37 / ubag2.polytronx.com) — generated 2026-09-13.
# Never commit. Secrets unique to this box (NOT shared with the primary).

UBAG_APP_SECRET=$APP_SECRET
UBAG_API_VERSION=2026-05-22
UBAG_GATEWAY_VERSION=0.0.0-vps2
UBAG_BUILD_COMMIT=$SHA
UBAG_ACTOR_ROLE=superadmin

POSTGRES_DB=ubag
POSTGRES_USER=ubag
POSTGRES_PASSWORD=$PG_PW
UBAG_POSTGRES_DSN=postgres://ubag:$PG_PW@postgres:5432/ubag?sslmode=disable

UBAG_PAT_ENABLED=true
UBAG_FACADE_MAX_WAIT_MS=240000
UBAG_GATEWAY_CPUS=1.00

UBAG_WORKER_POLL_INTERVAL_MS=75
UBAG_WORKER_DAEMON=true
UBAG_WORKER_MAX_RUNTIME_MS=1500000

UBAG_CHAT_LEDGER_ENABLED=true
UBAG_CHAT_REAPER_ENABLED=true
EOF
  chmod 600 deploy/vps/env.local
  echo "   written deploy/vps/env.local (600)"
fi

echo "== 5. dedicated Nginx Proxy Manager edge"
mkdir -p /opt/docker/nginx-proxy-manager
cat > /opt/docker/nginx-proxy-manager/docker-compose.yml <<EOF
services:
  app:
    image: jc21/nginx-proxy-manager:latest
    container_name: nginx-proxy-manager
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "81:81"
    volumes:
      - ./data:/data
      - ./letsencrypt:/etc/letsencrypt
EOF
cd /opt/docker/nginx-proxy-manager && docker compose up -d

echo "== 6. done — ubag stack NOT started yet (build happens next)"
