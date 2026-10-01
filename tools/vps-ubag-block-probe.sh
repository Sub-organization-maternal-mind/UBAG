#!/bin/bash
# Read-only: distinguish Cloudflare/bot-block 403 from a full network block.
set -u
B=ubag-vps-browser
UA='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'

probe() {
  url="$1"
  out=$(docker exec "$B" sh -c "wget -qO- --server-response --timeout=20 --tries=1 --header='User-Agent: $UA' '$url' 2>&1" 2>/dev/null)
  code=$(echo "$out" | grep -m1 'HTTP/' | awk '{print $2}')
  server=$(echo "$out" | grep -i -m1 '^ *Server:' | tr -d '\r')
  cf=$(echo "$out" | grep -i -m1 'cf-mitigated\|cf-ray\|cf-chl\|cloudflare' | head -c 80)
  printf '  %-38s HTTP %-5s %s %s\n' "$url" "${code:-none}" "$server" "$cf"
}

echo "=== with a real-browser User-Agent ==="
probe https://chatgpt.com/
probe https://claude.ai/
probe https://chat.deepseek.com/
probe https://chat.mistral.ai/
probe https://www.perplexity.ai/
probe https://gemini.google.com/
probe https://duck.ai/

echo
echo "=== raw DNS + TLS reachability (is it network or app layer?) ==="
for host in chatgpt.com claude.ai chat.deepseek.com gemini.google.com duck.ai; do
  ip=$(docker exec "$B" sh -c "getent hosts $host | head -1 | awk '{print \$1}'" 2>/dev/null)
  printf '  %-24s dns=%s\n' "$host" "${ip:-FAIL}"
done
