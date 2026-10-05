# Multimodal and voice examples

The runnable examples live next to each language. Every example reads
`UBAG_TOKEN` (an app secret or PAT, kept server-side) and optionally
`UBAG_BASE_URL` (default `http://127.0.0.1:8080`). Without file arguments the
media examples generate a 1x1 PNG and half a second of silence, so they run
with no assets.

| Example | JavaScript | Python | Go | HTTP (curl + jq) |
| --- | --- | --- | --- | --- |
| Capability discovery | `javascript/capabilities.mjs` | `python/capabilities.py` | `go/capabilities` | `http/capabilities.sh` |
| Multimodal chat (text + image + audio) | `javascript/multimodal-chat.mjs` | `python/multimodal_chat.py` | `go/multimodal` | `http/multimodal-chat.sh` |
| Utterance mode (audio in, text out) | `javascript/utterance.mjs` | `python/utterance.py` | `go/utterance` | `http/utterance.sh` |
| Backend voice-session management | `javascript/voice-sessions.mjs` | `python/voice_sessions.py` | `go/voicesessions` | `http/voice-session.sh` |
| Browser microphone (live WebRTC) | `javascript/voice-browser.html` + `javascript/voice-backend.mjs` | | | |

Setup per language:

- JavaScript: `pnpm --filter @ubag/sdk build`, then `node examples/javascript/<file>`.
  `javascript/sdk.mjs` is the single import point; in your own app import
  `@ubag/sdk` instead.
- Python: `pip install -e packages/sdk-python` (standard library only), then
  `python examples/python/<file>`.
- Go: `cd examples/go && go run ./capabilities` (the module uses a local
  `replace` for `packages/sdk-go`).
- HTTP: `examples/http/*.sh` need `curl` and `jq`.

## Browser voice

`voice-browser.html` captures the microphone with `getUserMedia`, builds an
`RTCPeerConnection` through the SDK's `VoiceMediaClient`, exchanges the SDP
offer through your backend, opens the `control` data channel, authenticates it
with the `media_credential` and offers a mute button. The page only talks to
`voice-backend.mjs`, a tiny Node server that holds the API key, keeps the
session lease alive and refuses to touch sessions it did not create. The
browser receives the SDP answer, `ice_servers` and the short-lived media
credential, never the API key.

```bash
UBAG_TOKEN=... node examples/javascript/voice-backend.mjs   # then open http://127.0.0.1:3000
```

Prerequisites: a gateway with voice enabled and a target whose operator-owned
profile is logged in by a human. UBAG never logs in for you. Check
`GET /v1/capabilities` (`voice.available`) first.

Guides: [multimodal requests](../apps/docs/src/content/docs/guides/multimodal.md),
[live voice](../apps/docs/src/content/docs/guides/live-voice.md),
[remote deployment and TURN](../apps/docs/src/content/docs/guides/voice-remote-deployment.md),
[capability discovery](../apps/docs/src/content/docs/guides/capabilities.md).

Offline check (syntax of every example plus a backend smoke test):
`node tools/check-examples.mjs`.
