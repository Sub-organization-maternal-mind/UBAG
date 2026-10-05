#!/bin/bash
# Read-only UBAG diagnostic probe. Runs on the VPS. No load, no writes.
# Env: only ALLOW values are printed; every other UBAG_* var is shown as NAME (len=N) only.
set -u
G=${UBAG_PROBE_GATEWAY:-ubag-vps-gateway-1}
B=${UBAG_PROBE_BROWSER:-ubag-vps-browser}

# Non-secret flags/sizing whose values may be printed. Add here only after review.
ALLOW=" UBAG_BUILD_COMMIT UBAG_EXECUTOR_MODE UBAG_WORKER_DAEMON UBAG_WORKER_CONCURRENCY UBAG_WORKER_POLL_INTERVAL_MS UBAG_GATEWAY_STORE UBAG_VOICE_STORE UBAG_VOICE_SESSIONS_ENABLED GOMAXPROCS GOMEMLIMIT "

probe_env() { # $1 = container
  docker exec "$1" env 2>/dev/null | grep -E '^(UBAG_|GOMAXPROCS=|GOMEMLIMIT=)' | sort | while IFS= read -r line; do
    name=${line%%=*}; val=${line#*=}
    case "$ALLOW" in
      *" $name "*) echo "  $name = $val" ;;
      *) echo "  $name (len=${#val})" ;;
    esac
  done
}

echo "======== gateway env (allowlisted values; others name+length) ========"
probe_env "$G"
echo
echo "======== browser env (allowlisted values; others name+length) ========"
probe_env "$B"

echo
echo "======== container limits ========"
for c in "$G" "$B"; do
  docker inspect -f '  {{.Name}} NanoCpus={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}} PidsLimit={{.HostConfig.PidsLimit}}' "$c" 2>/dev/null
done

echo
echo "======== host ========"
docker stats --no-stream --format '  {{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}' "$G" "$B" 2>/dev/null
free -m 2>/dev/null | sed 's/^/  /'
echo "  $(uptime 2>/dev/null)"

echo
echo "======== can the browser reach the providers? ========"
for url in https://chatgpt.com/ https://claude.ai/ https://gemini.google.com/ https://chat.deepseek.com/ https://duck.ai/ https://chat.mistral.ai/; do
  code=$(docker exec "$B" sh -c "wget -qO- --server-response --timeout=20 --tries=1 '$url' 2>&1 | grep -m1 'HTTP/' | awk '{print \$2}'" 2>/dev/null)
  echo "  $url -> ${code:-NO-RESPONSE}"
done

echo
echo "======== recent gateway log: keyword counts only (no log content) ========"
docker logs "$G" --since 4h 2>&1 | grep -ioE "selector|drift|traceback|manual_action|error" | tr 'A-Z' 'a-z' | sort | uniq -c | sed 's/^/  /'
