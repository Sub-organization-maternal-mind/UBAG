---
title: Live Voice
description: Two-way voice sessions over WebRTC against a provider's own voice mode, with lifecycle, statuses, queueing, mute, cleanup and limits.
---

A live voice session connects your application's microphone and speaker to a
provider's own voice mode running in a user-owned browser profile. Opus audio
travels over WebRTC to the gateway, which relays it to that browser's virtual
microphone and returns the provider's spoken reply as an audio track.
Signaling is plain HTTP, so any HTTP client can set a session up.

Safe mode applies: UBAG never logs in. A human signs in to the provider
once in the operator-owned profile, and capability discovery then reports the
account as available. Check [capabilities](/guides/capabilities) (`voice.available`)
before opening a session.

## Lifecycle

```text
POST /v1/voice/sessions            -> 201 connecting | 202 queued | 429 queue full
POST /v1/voice/sessions/{id}/connect {sdp_offer}
                                   -> {sdp_answer, media_credential, ice_servers}
   client: open the "control" data channel, send {"op":"auth","credential":...}
POST /v1/voice/sessions/{id}/mute  {muted}      (also on the control channel)
POST /v1/voice/sessions/{id}/renew              (heartbeat)
POST /v1/voice/sessions/{id}/terminate          (or DELETE /v1/voice/sessions/{id})
```

Create takes `target`, an optional `identity_ref` (a preference among your own
provider accounts, never proof of ownership), an optional `ttl_seconds`
(30..3600) and `mode` (`live`, the default, or `utterance`). Every action
under `/{id}/` is POST-only (`405` with `Allow` otherwise), and `GET /v1/voice/sessions[/{id}]`
read state. Tenant and app come from your credential, never from the body.

## Statuses

| Status | Meaning |
| --- | --- |
| `queued` | Accepted but no provider account and browser environment is free. No leases held. |
| `connecting` | Leases claimed; media and the provider voice UI are coming up. |
| `connected` | Reported only once the provider voice UI is verified ready and the client media is up. |
| `terminated` | Ended. `last_error` says why when it was not a client request, for example `activation_failed: <state>`, `lease_expired`. |

A failed activation terminates the session instead of leaving a silent call
that looks connected. Treat `terminated` with a `last_error` as the
provider-side failure signal and open a new session.

## Queueing and limits

- Each tenant may hold a bounded number of active sessions (default 4,
  `UBAG_VOICE_MAX_SESSIONS_PER_TENANT`) and a bounded queue (default 32,
  `UBAG_VOICE_MAX_QUEUED_PER_TENANT`). Excess sessions queue rather than fail.
- A `queued` session claims a free account and environment when you call
  `connect`; if none is free, `connect` answers `409` with retry guidance and the
  session stays queued.
- A full queue answers `429` (`UBAG-VOICE-QUEUE-FULL-004`) with `Retry-After` and
  `retry_after_ms`. Every `429`/`503` from the gateway carries both. Back off for
  that long; do not hammer.
- An account and an environment each serve one live session at a time (exclusive
  leases). The lease window is `ttl_seconds` (default 600 s,
  `UBAG_VOICE_SESSION_TTL_SECONDS`) and must be renewed.
- With `UBAG_VOICE_LANE_EXCLUSION=true` (opt-in, off by default) a live session
  also owns its browser: ordinary text and file jobs that drive the same shared
  browser wait (they are held back in the queue, not failed) until the session
  ends, and a session cannot take a browser while a job is running on it (it
  queues, or `connect` answers `409`). Dedicate a browser to voice if text jobs
  must keep flowing during calls.
- Request bodies are bounded (SDP up to 128 KiB, others far smaller; larger is
  `413`). `renew` of an expired or terminated session is `409`.

## Media

The client creates an `RTCPeerConnection`, adds the microphone track, creates a
data channel labelled `control` before the offer, waits for ICE gathering and
posts the offer to `connect`. The gateway answers with the SDP answer
(candidates embedded), the `ice_servers` to apply (TURN credentials are
time-limited) and a short-lived `media_credential` scoped to tenant, app and
session. The first message on the `control` channel must be
`{"op":"auth","credential":"<media_credential>"}`; control commands are ignored
until the gateway answers `{"event":"auth","ok":true}`.

| Client message | Effect |
| --- | --- |
| `{"op":"auth","credential":...}` | Authorizes the channel. Send first. |
| `{"op":"mute","muted":true}` | Mutes or unmutes the microphone path. |
| `{"op":"ping"}` | Answers `{"event":"pong"}`. |

Gateway events: `status` (`state`, `muted`), `auth` (`ok`), `mute` (`muted`),
`pong`, `error` (`reason`, for example `unauthorized`).

**Interruption (barge-in) is the provider's own.** UBAG relays audio both ways
and does no voice-activity detection or echo handling of its own. If you speak
while the provider is talking, the provider's voice mode decides whether to
stop. `mute` only controls your microphone direction; the provider can keep
speaking while you are muted.

## Browser helper

In a browser, use the SDK's `VoiceMediaClient`. It performs the offer/answer
exchange, applies `ice_servers`, opens the `control` channel and authenticates
it. It must never hold your API key: `exchange` calls **your backend**, which
calls `connectVoiceSession` with the key held server-side.

```js
import { VoiceMediaClient } from "@ubag/sdk";

const media = new VoiceMediaClient({
  stream: await navigator.mediaDevices.getUserMedia({ audio: true }),
  exchange: (sdp) => fetch(`/api/voice/${id}/connect`, { method: "POST", body: JSON.stringify({ sdp_offer: sdp }) }).then((r) => r.json()),
  onTrack: (e) => (audioEl.srcObject = e.streams[0]),
});
await media.connect();
await media.ready;      // control channel authenticated
media.mute(true);
media.close();          // then terminate the session on your backend
```

The complete page and backend are `examples/javascript/voice-browser.html` and
`voice-backend.mjs`. A browser calling the gateway directly from another origin
is rejected with `403 UBAG-AUTHZ-ORIGIN-005` unless the origin is allow-listed
(see [remote deployment](/guides/voice-remote-deployment)); server-to-server
calls send no `Origin` and are unaffected.

## Cleanup

- Always call `terminate` (idempotent). It releases the account and environment
  leases and drops the media path immediately.
- A client that vanishes without terminating cannot pin an account past the
  lease window: heartbeats stop and the sweeper reaps the session. Heartbeat
  with `renew` roughly every minute while a call is running.
- Closing the peer connection ends the media path, but still terminate the
  session so the leases are released at once.

## Utterance mode

`mode: "utterance"` is a separate, explicitly selected mode for audio in, text
out. It creates a transcription-style job instead of holding live leases: the
response carries `job_id`; `PUT` the audio to
`/v1/jobs/{job_id}/artifacts/utterance.wav`, then poll `GET /v1/jobs/{job_id}` for
the transcript. A live session is never silently substituted with an
utterance job; unknown modes are rejected.

## SDKs and examples

| | Calls |
| --- | --- |
| TypeScript | `createVoiceSession`, `connectVoiceSession`, `muteVoiceSession`, `renewVoiceSessionLease`, `terminateVoiceSession`, `listVoiceSessions`, `getVoiceSession`, `VoiceMediaClient` |
| Go | `CreateVoiceSession`, `ConnectVoiceSession`, `MuteVoiceSession`, `RenewVoiceSessionLease`, `TerminateVoiceSession`, `ListVoiceSessions`, `GetVoiceSession` (typed structs, `ICEServer`) |
| Python | `create_voice_session`, `connect_voice_session`, `mute_voice_session`, `renew_voice_session`, `terminate_voice_session`, `list_voice_sessions`, `get_voice_session` |

All examples are indexed in `examples/voice-sessions.md`.
