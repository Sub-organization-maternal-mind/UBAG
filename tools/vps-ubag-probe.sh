#!/bin/bash
# Read-only UBAG diagnostic probe. Runs on the VPS.
set -u
G=ubag-vps-gateway-1
B=ubag-vps-browser

echo "======== ambient ========"
echo "EXECUTOR_MODE     = $(docker exec $G printenv UBAG_EXECUTOR_MODE 2>/dev/null)"
echo "WORKER_SCRIPT     = $(docker exec $G printenv UBAG_WORKER_SCRIPT 2>/dev/null)"
echo "WORKER_PYTHON     = $(docker exec $G printenv UBAG_WORKER_PYTHON 2>/dev/null)"
echo "WORKER_DAEMON     = $(docker exec $G printenv UBAG_WORKER_DAEMON 2>/dev/null)"
echo "REMOTE_BROWSER    = $(docker exec $G printenv UBAG_REMOTE_BROWSER_ENDPOINT 2>/dev/null)"
echo "GATEWAY_STORE     = $(docker exec $G printenv UBAG_GATEWAY_STORE 2>/dev/null)"

echo
echo "======== worker app layout (gateway image) ========"
docker exec $G sh -c 'ls /app 2>/dev/null | head -20; echo "---apps/worker---"; ls /app/apps/worker 2>/dev/null | head -20'

echo
echo "======== can the browser reach the providers? ========"
for url in https://chatgpt.com/ https://claude.ai/ https://gemini.google.com/ https://chat.deepseek.com/ https://duck.ai/ https://www.perplexity.ai/ https://chat.mistral.ai/; do
  code=$(docker exec $B sh -c "wget -qO- --server-response --timeout=20 --tries=1 '$url' 2>&1 | grep -m1 'HTTP/' | awk '{print \$2}'" 2>/dev/null)
  echo "  $url -> ${code:-NO-RESPONSE}"
done

echo
echo "======== recent worker/executor errors in gateway log ========"
docker logs $G --since 4h 2>&1 | grep -iE "selector|drift|worker|adapter|browser|manual_action|traceback|error" | tail -30
