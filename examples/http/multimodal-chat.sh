#!/usr/bin/env bash
# Multimodal chat: text + image + audio.   ./multimodal-chat.sh [target] image.png clip.wav
# Inline parts only: images as data URLs (png/jpeg/webp/gif), audio as base64 wav/mp3.
# Remote URLs are rejected. At most 10 inline parts, 24 MiB decoded per part.
set -euo pipefail
. "$(dirname "$0")/_env.sh"
target="${1:-chatgpt_web}"; image="${2:?image.png}"; audio="${3:?clip.wav}"

png_b64=$(base64 < "$image" | tr -d '\n')
wav_b64=$(base64 < "$audio" | tr -d '\n')
# Large payloads go through a file; shell argument limits are small.
body=$(mktemp); trap 'rm -f "$body"' EXIT
jq -n --arg model "$target" --arg png "$png_b64" --arg wav "$wav_b64" '{
  model: $model,
  messages: [{role: "user", content: [
    {type: "text", text: "Describe this image and transcribe the clip."},
    {type: "image_url", image_url: {url: ("data:image/png;base64," + $png)}},
    {type: "input_audio", input_audio: {data: $wav, format: "wav"}}
  ]}]
}' > "$body"

api POST /v1/openai/chat/completions -H "Content-Type: application/json" --data-binary "@$body" | jq -r '.choices[0].message.content'
