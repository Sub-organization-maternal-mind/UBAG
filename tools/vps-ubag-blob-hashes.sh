#!/bin/bash
# Read-only: emit git blob SHA-1 for key UBAG files, so they can be compared
# against the same files in a git checkout of a known commit.
set -u
cd /opt/docker/ubag || exit 0
FILES="
LICENSE
AGENTS.md
Makefile
package.json
docker-compose.vps.yml
docker-compose.small.yml
adapters/registry.json
adapters/chatgpt_web/manifest.json
adapters/gemini_web/manifest.json
adapters/duckai_web/manifest.json
apps/worker/ubag_worker/live/engine.py
apps/worker/ubag_worker/live/selectors.py
apps/worker/ubag_worker/live/page_driver.py
apps/worker/ubag_worker/live/envelope.py
apps/worker/run_live_worker.py
apps/gateway/go.mod
"
for f in $FILES; do
  if [ -f "$f" ]; then
    printf '%s %s\n' "$(git hash-object "$f" 2>/dev/null || sha1sum "$f" | cut -d' ' -f1)" "$f"
  else
    printf 'MISSING %s\n' "$f"
  fi
done
