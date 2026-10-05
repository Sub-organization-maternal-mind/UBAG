---
title: Voice Behind HTTPS and TURN
description: What a remote deployment needs for live voice - HTTPS signaling, allowed origins, the media UDP range and TURN for restrictive networks.
---

Voice has two network paths. Signaling is ordinary HTTP under `/v1/voice/*` and
rides your normal HTTPS front end. Media is WebRTC over UDP and needs its own
ports. The step-by-step portable profile, including Compose commands,
environment variables and the full port table, is in
[`deploy/small/README.md`](https://github.com/ubag/ubag/blob/main/deploy/small/README.md)
(section "Remote/HTTPS + voice"); this page explains the moving parts.

## Signaling

- Serve the gateway over HTTPS (the `tls` profile uses Caddy with automatic
  certificates). Browsers will not grant microphone access to a non-HTTPS page
  other than `localhost`.
- Browsers should not call the gateway with your API key. Call your own
  backend, which holds the key (see [live voice](/guides/live-voice#browser-helper)).
- If a browser does call `/v1/voice/*` directly from another origin, list the
  exact origin in `UBAG_ALLOWED_ORIGINS`; otherwise the answer is
  `403 UBAG-AUTHZ-ORIGIN-005`. Server-to-server callers send no `Origin` and are
  unaffected.

## Media

- Audio uses a bounded UDP port range (default 50000-50039,
  `UBAG_VOICE_MEDIA_PORT_MIN` / `UBAG_VOICE_MEDIA_PORT_MAX`), published only by the
  opt-in overlay `compose.voice-media.yml`. A few ports per concurrent session is
  enough; keep the range as small as your session limit allows.
- Set `UBAG_VOICE_NAT_1TO1_IP` to the address clients actually reach (public IP,
  or a LAN IP for an internal deployment) so the gateway advertises a reachable
  candidate.
- CDP, VNC and the audio relay are never published; only the media
  UDP range and (optionally) TURN are.

## TURN for restrictive networks

Clients behind symmetric NAT or UDP-hostile firewalls need a relay. Enable the
`turn` profile and set `UBAG_VOICE_TURN_URLS` (for example
`turn:ubag.example.com:3478?transport=udp`) and `UBAG_VOICE_TURN_SECRET`. The
gateway mints time-limited TURN credentials per session and returns them in the
`connect` response `ice_servers`, so no long-lived relay password ships to
clients. Apply the returned `ice_servers` to the `RTCPeerConnection`; the SDK's
`VoiceMediaClient` does this for you. Credentials expire, so a stale page
reconnects by creating a new connect exchange rather than reusing old ones.

The bundled coturn has no TLS listener, so URLs are `turn:` not `turns:`; it
refuses to relay to private ranges except the gateway's media address.

## Checklist

1. HTTPS in front of `/v1/*`, `UBAG_ALLOWED_ORIGINS` set only if browsers call it.
2. `UBAG_VOICE_AUDIO_ENABLED=1` and a private `UBAG_VOICE_RELAY_SECRET`.
3. Media UDP range open and `UBAG_VOICE_NAT_1TO1_IP` set.
4. TURN profile and secret if users sit behind restrictive networks.
5. `GET /v1/capabilities` reports `voice.configured: true` and a free account.
