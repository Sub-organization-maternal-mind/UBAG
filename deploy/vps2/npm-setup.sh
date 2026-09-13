#!/bin/sh
# NPM one-time setup for vps2 — runs ON 213.163.201.37 (API on 127.0.0.1:81).
# Prints only status codes / non-secret metadata. Admin password lands in
# /opt/docker/nginx-proxy-manager/ADMIN-CREDENTIALS.txt (root-only).
set -e
BASE=http://127.0.0.1:81
EMAIL=admin@polytronx.com

echo "== wait for API"
i=0
while [ $i -lt 30 ]; do
  if curl -s -m 2 $BASE/api/ | grep -q OK; then break; fi
  sleep 2
  i=$((i+1))
done
curl -s -m 5 $BASE/api/

STATUS=$(curl -s -m 5 $BASE/api/ | sed -n 's/.*"setup":\([a-z]*\).*/\1/p')
echo "setup-flag:$STATUS"

PW=$(openssl rand -hex 12)

if [ "$STATUS" = "false" ]; then
  echo "== create admin user (setup mode: POST /api/users)"
  CODE=$(curl -s -m 20 -o /tmp/npm-setup.json -w '%{http_code}' -X POST $BASE/api/users \
    -H 'Content-Type: application/json' \
    --data "{\"email\":\"$EMAIL\",\"name\":\"admin\",\"nickname\":\"admin\",\"roles\":[\"admin\"],\"auth\":{\"type\":\"password\",\"secret\":\"$PW\"}}")
  echo "setup:$CODE"
  [ "$CODE" = "201" ] || { head -c 300 /tmp/npm-setup.json; echo; exit 1; }
fi

echo "== login"
LOGIN=$(curl -s -m 15 -X POST $BASE/api/tokens \
  -H 'Content-Type: application/json' \
  --data "{\"identity\":\"$EMAIL\",\"secret\":\"$PW\"}")
TOKEN=$(echo "$LOGIN" | sed -n 's/.*"token":"\([A-Za-z0-9._-]*\)".*/\1/p')
[ -n "$TOKEN" ] || { echo login-failed; echo "$LOGIN" | head -c 300; echo; exit 1; }
echo "login:ok"

umask 077
printf 'admin-email: %s\nadmin-password: %s\n' "$EMAIL" "$PW" \
  > /opt/docker/nginx-proxy-manager/ADMIN-CREDENTIALS.txt
echo "creds saved: /opt/docker/nginx-proxy-manager/ADMIN-CREDENTIALS.txt (600)"

echo "== proxy host + LE cert for ubag2.polytronx.com"
CODE=$(curl -s -m 120 -o /tmp/npm-ph.json -w '%{http_code}' -X POST $BASE/api/nginx/proxy-hosts \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  --data "{\"domain_names\":[\"ubag2.polytronx.com\"],\"forward_scheme\":\"http\",\"forward_host\":\"ubag-vps2-nginx-dashboard\",\"forward_port\":80,\"block_exploits\":true,\"allow_websocket_upgrade\":true,\"http2_support\":true,\"ssl_forced\":true,\"hsts_enabled\":false,\"caching_enabled\":false,\"certificate_id\":\"new\",\"meta\":{\"letsencrypt_email\":\"$EMAIL\",\"letsencrypt_agree\":true,\"dns_challenge\":false},\"locations\":[]}")
echo "proxyhost:$CODE"
if [ "$CODE" != "200" ] && [ "$CODE" != "201" ]; then
  head -c 500 /tmp/npm-ph.json; echo; exit 1
fi
echo "proxy-host-created"
