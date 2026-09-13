#!/bin/sh
# NPM (v2.15) setup record for vps2 — 213.163.201.37, ubag2.polytronx.com.
# Runs ON the VPS against the local admin API (127.0.0.1:81). This is the
# exact sequence that was executed live on 2026-09-13; kept as the reference
# for rebuilding the box. Prints only status codes / non-secret metadata.
#
# v2.15 API gotchas learned the hard way:
#   - No POST /api/setup: the first admin is created with POST /api/users
#     while in setup mode (schema: name, nickname, email, roles, and the
#     password inside auth:{type:"password",secret}).
#   - The LE contact email is taken from the requesting ADMIN USER's email —
#     there is no letsencrypt_email anywhere (settings are GET-only).
#   - POST /api/nginx/certificates meta allows only: dns_challenge,
#     dns_provider*, propagation_seconds, key_type, certificate(_key),
#     letsencrypt_certificate. No letsencrypt_email/agree.
#   - Do NOT inject a /.well-known/acme-challenge/ location via
#     advanced_config: the proxy-host template adds it itself once a
#     certificate is attached — duplicating it makes nginx -t fail and NPM
#     silently ROLLS BACK by deleting the generated conf (only visible with
#     DEBUG=true). advanced_config must stay empty.
#   - API JSON may be pretty-printed — parse defensively.
set -e
BASE=http://127.0.0.1:81
EMAIL=admin@polytronx.com

# 0. login (admin created below on first run)
PW=$(sed -n 's/^admin-password: //p' /opt/docker/nginx-proxy-manager/ADMIN-CREDENTIALS.txt 2>/dev/null || true)
if [ -z "$PW" ]; then
  PW=$(openssl rand -hex 12)
fi

STATUS=$(curl -s -m 5 $BASE/api/ | tr -d ' \n' | sed -n 's/.*"setup":\([a-z]*\).*/\1/p')
echo setup-flag:$STATUS

if [ "$STATUS" = "false" ]; then
  CODE=$(curl -s -m 20 -o /tmp/npm-setup.json -w '%{http_code}' -X POST $BASE/api/users \
    -H 'Content-Type: application/json' \
    --data "{\"email\":\"$EMAIL\",\"name\":\"admin\",\"nickname\":\"admin\",\"roles\":[\"admin\"],\"auth\":{\"type\":\"password\",\"secret\":\"$PW\"}}")
  echo setup:$CODE
  [ "$CODE" = "201" ] || { head -c 300 /tmp/npm-setup.json; echo; exit 1; }
  umask 077
  printf 'admin-email: %s\nadmin-password: %s\n' "$EMAIL" "$PW" \
    > /opt/docker/nginx-proxy-manager/ADMIN-CREDENTIALS.txt
fi

TOKEN=$(curl -s -m 15 -X POST $BASE/api/tokens -H 'Content-Type: application/json' --data "{\"identity\":\"$EMAIL\",\"secret\":\"$PW\"}" | tr -d ' \n' | sed -n 's/.*"token":"\([A-Za-z0-9._-]*\)".*/\1/p')
[ -n "$TOKEN" ] || { echo login-failed; exit 1; }
echo login:ok

# 1. proxy host (http only at this point — cert needs working DNS first)
CODE=$(curl -s -m 20 -o /tmp/ph.json -w '%{http_code}' -X POST $BASE/api/nginx/proxy-hosts \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  --data '{"domain_names":["ubag2.polytronx.com"],"forward_scheme":"http","forward_host":"ubag-vps2-nginx-dashboard","forward_port":80,"block_exploits":true,"allow_websocket_upgrade":true,"http2_support":true,"ssl_forced":false,"hsts_enabled":false,"caching_enabled":false,"certificate_id":0,"meta":{"dns_challenge":false},"locations":[]}')
echo proxyhost:$CODE

# 2. request the LE certificate (HTTP-01 through Cloudflare to origin :80)
CODE=$(curl -s -m 120 -o /tmp/cert.json -w '%{http_code}' -X POST $BASE/api/nginx/certificates \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  --data '{"provider":"letsencrypt","nice_name":"ubag2.polytronx.com","domain_names":["ubag2.polytronx.com"],"meta":{"dns_challenge":false}}')
echo cert-request:$CODE
CERT_ID=$(tr -d ' \n' < /tmp/cert.json | sed -n 's/.*{"id":\([0-9]*\),.*/\1/p' | head -1)
[ -n "$CERT_ID" ] || { echo no-cert-id; exit 1; }
echo cert-id:$CERT_ID

# 3. attach cert + force SSL + HTTP/2 (advanced_config MUST stay empty)
CODE=$(curl -s -m 15 -o /tmp/ph.json -w '%{http_code}' -X PUT $BASE/api/nginx/proxy-hosts/1 \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  --data "{\"certificate_id\":$CERT_ID,\"ssl_forced\":true,\"http2_support\":true,\"advanced_config\":\"\"}")
echo attach:$CODE
grep -o '"ssl_forced":[a-z]*' /tmp/ph.json

# 4. verify (origin-local)
curl -sk -m 8 --resolve ubag2.polytronx.com:443:127.0.0.1 -o /dev/null -w 'local-https:%{http_code}\n' https://ubag2.polytronx.com/
