---
title: Capability Discovery
description: Ask the gateway what each target accepts (images, audio, attachments) and whether live voice can start right now.
---

`GET /v1/capabilities` (action `job:read`) publishes one record per target so an
application can check support before it submits a multimodal request or opens
a voice session. Everything in it comes from the same sources the enforcement
paths use, so the advertisement cannot promise what create-time validation
would refuse.

```bash
curl -sS -H "Authorization: Bearer $UBAG_TOKEN" \
  http://127.0.0.1:8080/v1/capabilities | jq '.data[] | {target, inline_message_parts, voice}'
```

## Record shape

| Field | Meaning |
| --- | --- |
| `target`, `display_name`, `kind` | Target key (use it as the facade `model` or the voice `target`), label and adapter kind. |
| `safe_mode`, `manual_login_required` | Safe mode is a hard constraint: sessions are user-owned and a human logs in. `manual_login_required` stays authoritative. |
| `attachments` | The native attachment policy (`max_files`, `max_file_bytes`, `accepted[{kind, content_types}]`), same shape as `GET /v1/adapters`. |
| `audio` | What this target can actually DO with audio, below. |
| `inline_message_parts` | Inline content parts the OpenAI facade accepts for this target: `image_url` (accepted image MIME types), `input_audio` (accepted audio MIME types) and `remote_urls` (always `false`). An empty list means the target rejects those bytes. |
| `voice` | Voice support, below. |

## The `audio` object

A provider's composer usually accepts more than its model can read, so
"the file was attached" is not "the audio was processed". This object
publishes the measured contract.

| Field | Meaning |
| --- | --- |
| `formats` | The audio containers the provider's model can decode. Measured, not guessed: a container that the composer accepts but the model drops (ChatGPT accepted `audio/webm`, `audio/ogg` and `audio/mp4` and then answered "No audio attachment is accessible in this message.") is not listed. |
| `kind` | The attachment kind to declare for audio on this target (`voice` or `audio`). |
| `models` | The provider models that can ingest audio here, when the target makes that claim. On Gemini only `3.8 Flash` could; `4 Argon` answered "no audio attached" every time. A job pinned to a model outside this list is refused at create time with `UBAG-VALIDATION-AUDIO-MODEL-UNAVAILABLE-001`. |
| `verified` | The measurement note behind the claim. |

Send one of the listed `formats`. A container outside the list is refused at
create time (`UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001`) with the verified
list in the message, rather than travelling to the provider and coming back
as a confident wrong answer. If a provider still drops a file that IS in the
list, the job fails with `provider_audio_not_ingested` instead of reporting
success.

## The `voice` object

The flags are separate on purpose, so a manifest declaration is never mistaken
for a working capability:

| Field | True when |
| --- | --- |
| `supported` | The provider adapter declares a live voice entry control. |
| `configured` | This gateway has the voice store, media plane and provider activation wired. |
| `verified` | A live two-way acceptance run is recorded for the provider. `verified_note` carries the text. |
| `available` | `configured` and at least one authenticated account with a free browser environment exists for your tenant right now. |
| `free_resources` | Number of free account + environment pairs (how many live sessions could start now). |
| `utterance_jobs` | The target accepts audio for [utterance mode](/guides/live-voice#utterance-mode). |
| `live_entry_control` | Which provider UI control starts voice. |

`live` (= `supported`) and `available_accounts` (= `free_resources`) are legacy
aliases kept for older clients.

Availability is a snapshot. Another session can claim the last free account
between your check and your create, and a provider login can lapse between jobs,
so still handle a `queued` session or a `429` (see [Live voice](/guides/live-voice)).

## SDKs

```ts
const { data } = await client.listCapabilities();
const target = data.find((t) => t.target === "chatgpt_web");
if (target?.voice.available) { /* open a session */ }
```

```go
caps, _ := client.ListCapabilities(ctx)
if t, ok := caps.Find("chatgpt_web"); ok && t.Voice.Available { /* open a session */ }
```

```python
if (client.capability("chatgpt_web") or {}).get("voice", {}).get("available"):
    ...
```

Runnable versions: `examples/javascript/capabilities.mjs`, `examples/python/capabilities.py`,
`examples/go/capabilities`, `examples/http/capabilities.sh`.
