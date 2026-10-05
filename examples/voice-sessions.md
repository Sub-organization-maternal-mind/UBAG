# Voice sessions — multimodal release examples

Discover what a target accepts, then open a two-way live voice session or run
an utterance (audio-in/text-out) job. The provider website is the backend: the
client speaks over WebRTC, the gateway relays Opus frames to the browser
container's virtual microphone, and the provider's spoken reply is captured
from the sink monitor and returned as an audio track.

Prerequisites:

- A gateway with voice enabled (`UBAG_VOICE_STORE` set) and a browser/audio
  environment with `UBAG_VOICE_AUDIO_ENABLED=1`.
- A live-voice target (`chatgpt_web`, `gemini_web`) whose operator-owned
  profile is logged in by a human. UBAG never logs in.
- Authenticate exactly like the REST API (Bearer app secret / PAT).

## 1. Discover capabilities (any HTTP client)

```bash
curl -sS -H "Authorization: Bearer $UBAG_TOKEN" \
  https://gateway.example.com/v1/capabilities | jq '.data[] | {target, voice, inline_message_parts}'
```

Only targets whose `voice.live` is `true` accept live sessions. The facade's
inline message parts (`image_url`, `input_audio`) are already intersected with
each target's attachment policy — if a part family is listed empty, the target
rejects those bytes at create time.

## 2. Live voice session (JavaScript / TypeScript)

```ts
import { UbagClient } from "@ubag/sdk";

const client = new UbagClient({ baseURL: "https://gateway.example.com", token: process.env.UBAG_TOKEN });

const { session } = await client.createVoiceSession({ target: "chatgpt_web" });
if (session.status === "queued") {
  // No provider account / browser environment free yet. Poll getVoiceSession
  // or simply retry connect later — connect claims leases when one frees up.
}

// Browser side: create the RTCPeerConnection, add your microphone track,
// then exchange the offer through the gateway (HTTP-only signaling; the
// answer embeds every ICE candidate).
const pc = new RTCPeerConnection();
pc.addTrack(micTrack, stream);
const offer = await pc.createOffer();
await pc.setLocalDescription(offer);
const connect = await client.connectVoiceSession(session.session_id, offer.sdp!);
await pc.setRemoteDescription({ type: "answer", sdp: connect.sdp_answer! });

// Keep the lease alive while the session runs; a lapsed lease releases the
// provider account (crash-safe by design).
const heartbeat = setInterval(() => client.renewVoiceSessionLease(session.session_id), 60_000);

// Mute the client's mic direction without ending the session.
await client.muteVoiceSession(session.session_id, true);

// When finished: terminate releases the leases and drops the media path.
clearInterval(heartbeat);
await client.terminateVoiceSession(session.session_id);
```

## 3. Live voice session (Go)

```go
client := ubaggo.NewClient("https://gateway.example.com", ubaggo.WithToken(os.Getenv("UBAG_TOKEN")))

created, _ := client.CreateVoiceSession(ctx, ubaggo.JSON{"target": "gemini_web"})
session := created["session"].(map[string]any)

answer, _ := client.ConnectVoiceSession(ctx, session["session_id"].(string),
    ubaggo.JSON{"sdp_offer": offerSDP})
_ = answer["sdp_answer"] // set as the peer connection's remote description

_ = client.RenewVoiceSessionLease(ctx, session["session_id"].(string))
_, _ = client.TerminateVoiceSession(ctx, session["session_id"].(string))
```

## 4. Live voice session (Python, generic HTTP)

```python
import json, os, urllib.request

BASE = "https://gateway.example.com"
TOKEN = os.environ["UBAG_TOKEN"]

def call(method, path, payload=None):
    req = urllib.request.Request(
        BASE + path,
        method=method,
        data=json.dumps(payload).encode() if payload is not None else None,
        headers={"Authorization": f"Bearer {TOKEN}", "Content-Type": "application/json"},
    )
    return json.loads(urllib.request.urlopen(req).read())

created = call("POST", "/v1/voice/sessions", {"target": "chatgpt_web"})
sid = created["session_id"]
answer = call("POST", f"/v1/voice/sessions/{sid}/connect", {"sdp_offer": offer_sdp})
# hand answer["sdp_answer"] to aiortc/webrtcclients as the remote description
call("POST", f"/v1/voice/sessions/{sid}/terminate")
```

## 5. Utterance mode (audio-in / text-out, no live leases)

Utterance mode is a SEPARATE, explicitly selected mode — a live session is
never substituted with one. It creates a transcription-style job: upload the
audio, poll the job, read the transcript.

```bash
# create the session + backing job
curl -sS -X POST -H "Authorization: Bearer $UBAG_TOKEN" -H "Content-Type: application/json" \
  -d '{"target":"chatgpt_web","mode":"utterance"}' \
  https://gateway.example.com/v1/voice/sessions
# → {"session_id":"voice_...","job_id":"job_...","next":"PUT the audio to ..."}

# upload the audio (any provider-accepted audio MIME), then poll the job
curl -sS -X PUT -H "Authorization: Bearer $UBAG_TOKEN" -H "Content-Type: audio/wav" \
  --data-binary @utterance.wav \
  https://gateway.example.com/v1/jobs/job_.../artifacts/utterance.wav
curl -sS -H "Authorization: Bearer $UBAG_TOKEN" \
  https://gateway.example.com/v1/jobs/job_...
```

The OpenAI-compatible facade offers the same audio-in/text-out behavior in the
standard chat shape: `POST /v1/openai/chat/completions` with
`input_audio` content parts (base64 wav/mp3), or
`POST /v1/openai/audio/transcriptions` for pure transcription.

## 6. Mixed text + image + audio on the facade

```bash
curl -sS -X POST -H "Authorization: Bearer $UBAG_TOKEN" -H "Content-Type: application/json" \
  -d '{
    "model": "chatgpt_web",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "Describe this image and transcribe the clip."},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,'"$PNG_B64"'"}},
        {"type": "input_audio", "input_audio": {"data": "'"$WAV_B64"'", "format": "wav"}}
      ]
    }]
  }' https://gateway.example.com/v1/openai/chat/completions
```

Limits: at most 10 inline parts per request, 24 MiB decoded per part, and a
48 MiB request body (override with `UBAG_FACADE_MAX_BODY_BYTES`). Remote media
URLs are rejected — inline base64 only.
