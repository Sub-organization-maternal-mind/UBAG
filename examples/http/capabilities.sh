#!/usr/bin/env bash
# Capability discovery: per-target media and voice support.   ./capabilities.sh
set -euo pipefail
. "$(dirname "$0")/_env.sh"

# voice.supported / configured / verified / available are separate booleans;
# voice.free_resources counts free account+environment pairs right now.
api GET /v1/capabilities | jq '.data[] | {target, inline_message_parts, voice}'
