---
title: Multimodal Requests
description: Send text, images and audio to a provider through the OpenAI-compatible chat endpoint, with limits, capability errors and SDK helpers.
---

`POST /v1/openai/chat/completions` accepts message `content` as an array of
parts, so one request can carry text, an image and an audio clip. The gateway
turns each non-text part into an attachment on the backing job, which means
the target's normal attachment policy and the provider's own limits apply
unchanged.

## Part types

| Part | Shape | Notes |
| --- | --- | --- |
| text | `{"type":"text","text":"..."}` | `input_text` is accepted as an alias. |
| image | `{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}` | Data URLs only: png, jpeg, webp, gif. Remote `https://` URLs are rejected, the gateway never fetches media. |
| audio | `{"type":"input_audio","input_audio":{"data":"<base64>","format":"wav"}}` | `format` is `wav` or `mp3`. |

```bash
curl -sS -X POST -H "Authorization: Bearer $UBAG_TOKEN" -H "Content-Type: application/json" \
  --data-binary @request.json http://127.0.0.1:8080/v1/openai/chat/completions
```

The answer is a normal `chat.completion`; `choices[0].message.content` holds
the provider's reply. Token usage is a character-based estimate.

## Limits

- At most 10 inline media parts per request, 24 MiB decoded per part.
- Request body up to 48 MiB (`UBAG_FACADE_MAX_BODY_BYTES` overrides). Base64
  inflates by about a third, so a 24 MiB part needs about 32 MiB of JSON.
- Inline parts share the 32-attachment job cap with any `ubag_attachments`
  you declare yourself.
- Unsupported combinations fail at create time with an OpenAI-shaped `400`
  naming the problem. Per-target support, including which MIME types are
  accepted, is published by [capability discovery](/guides/capabilities)
  (`inline_message_parts`): check it first instead of discovering a rejection.

## Errors

Facade errors are OpenAI-shaped (`{"error":{"message","type","code"}}`) rather
than the UBAG envelope. A missing credential still returns the gateway's
`UBAG-AUTH-MISSING-001` envelope. When the gateway is overloaded the answer is
`429` or `503` with a `Retry-After` header and `retry_after_ms`; the SDKs expose
both (`UbagApiError.retryAfterMs`, `APIError.RetryAfterMS()`,
`UbagError.retry_after_ms`), and the Python client retries them for you within a
bound.

## SDK helpers

```ts
import { UbagClient, textPart, imagePart, audioPart } from "@ubag/sdk";

const reply = await client.createChatCompletion({
  model: "chatgpt_web",
  messages: [{ role: "user", content: [
    textPart("Describe the image, then transcribe the clip."),
    imagePart(pngBytes, "image/png"),   // or imagePart("data:image/png;base64,...")
    audioPart(wavBytes, "wav"),
  ] }],
});
```

```go
reply, err := client.CreateChatCompletion(ctx, ubag.ChatCompletionRequest{
    Model: "chatgpt_web",
    Messages: []ubag.ChatMessage{{Role: "user", Content: []ubag.JSON{
        ubag.TextPart("Describe the image."),
        ubag.ImagePart("image/png", pngBytes),
        ubag.AudioPart(wavBytes, "wav"),
    }}},
})
```

```python
from ubag_client import UbagClient, text_part, image_part, audio_part

reply = client.chat_completion("chatgpt_web", [{"role": "user", "content": [
    text_part("Describe the image."), image_part(png_bytes, "image/png"), audio_part(wav_bytes, "wav")]}])
```

Runnable versions: `examples/javascript/multimodal-chat.mjs`,
`examples/python/multimodal_chat.py`, `examples/go/multimodal`,
`examples/http/multimodal-chat.sh`.

## Audio in, text out without a call

Audio clips can also be sent through [utterance mode](/guides/live-voice#utterance-mode)
or `POST /v1/openai/audio/transcriptions` (multipart) when you only need a
transcript.

## Which audio actually gets transcribed

Not every container reaches the model. A provider's composer accepts a file it
cannot decode, and the model then answers "No audio attachment is accessible
in this message." — a confident wrong answer, not an error. UBAG therefore
publishes the measured contract per target and enforces it:

- `GET /v1/capabilities` → `audio.formats` lists the containers the target's
  model can read, `audio.input_formats` lists the containers you may send
  (the rest are converted for you), and `audio.models` lists the models that
  can ingest audio at all.
- A container outside `audio.input_formats` is refused at create time with
  `UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001` and the accepted list in the
  message. Nothing is uploaded and no provider is billed.
- A model outside `audio.models` is refused with
  `UBAG-VALIDATION-AUDIO-MODEL-UNAVAILABLE-001`.
- If a provider still drops a file that passed both checks, the job fails with
  `provider_audio_not_ingested` rather than reporting a transcript that was
  never produced.

### Voice messages

A voice message is a short clip in whatever the sender's app produced:
webm/opus (WhatsApp, Telegram, Chrome's MediaRecorder), ogg/opus (Telegram)
or m4a/AAC (iOS voice memos). Providers disagree about which of those they
can decode — measured on the production account (2026-10-21), `chatgpt_web`
transcribed `audio/wav` and `audio/mpeg` and silently dropped `audio/mp4`,
`audio/ogg` and `audio/webm`, while `gemini_web` read `audio/webm`.

So UBAG converts the recording before it attaches it: the worker decodes it
with the browser it already drives and re-encodes it as 16-bit PCM WAV, the
container every measured provider reads. The conversion happens in a
throwaway tab and never touches the provider's page. A recording that cannot
be decoded at all fails the job with `audio_container_unsupported` instead of
being attached and quietly ignored.
