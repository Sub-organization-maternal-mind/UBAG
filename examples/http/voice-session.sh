#!/usr/bin/env bash
# Backend voice-session management over plain HTTP (media/WebRTC is the client's job;
# see examples/javascript/voice-browser.html).   ./voice-session.sh [target]
set -euo pipefail
. "$(dirname "$0")/_env.sh"
target="${1:-chatgpt_web}"
json=(-H "Content-Type: application/json")

# 201 = connecting, 202 = queued, 429 = budget/queue full (Retry-After header + retry_after_ms).
created=$(api POST /v1/voice/sessions "${json[@]}" -d "{\"target\":\"$target\"}")
id=$(jq -r .session_id <<<"$created")
trap 'api POST "/v1/voice/sessions/$id/terminate" > /dev/null' EXIT
echo "$created" | jq '{session_id, status}'

# connect = {sdp_offer} -> {sdp_answer, media_credential, ice_servers}; produced by the WebRTC client:
#   api POST "/v1/voice/sessions/$id/connect" "${json[@]}" -d '{"sdp_offer":"..."}'

api GET "/v1/voice/sessions/$id" | jq '.session | {status, muted, last_error}'
api POST "/v1/voice/sessions/$id/renew"                              # heartbeat, about every 60s
api POST "/v1/voice/sessions/$id/mute" "${json[@]}" -d '{"muted":true}'
api GET "/v1/voice/sessions?limit=10&target=$target" | jq '.data[] | {session_id, status}'
