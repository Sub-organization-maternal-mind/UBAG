#!/usr/bin/env bash
# Utterance mode: audio in, text out, no live voice leases.   ./utterance.sh [target] clip.wav
set -euo pipefail
. "$(dirname "$0")/_env.sh"
target="${1:-chatgpt_web}"; audio="${2:?clip.wav}"

created=$(api POST /v1/voice/sessions -H "Content-Type: application/json" -d "{\"target\":\"$target\",\"mode\":\"utterance\"}")
session=$(jq -r .session_id <<<"$created"); job=$(jq -r .job_id <<<"$created")
echo "session $session job $job"

# The job is held until the audio lands under the declared key.
api PUT "/v1/jobs/$job/artifacts/utterance.wav" -H "Content-Type: audio/wav" --data-binary "@$audio" > /dev/null

while :; do
  sleep 2
  job_json=$(api GET "/v1/jobs/$job"); status=$(jq -r .status <<<"$job_json")
  echo "job status: $status"
  case "$status" in completed|completed_with_warnings|failed_retryable|failed_terminal|dead_letter|cancelled|timed_out) break;; esac
done
jq .result <<<"$job_json"
api POST "/v1/voice/sessions/$session/terminate" > /dev/null
