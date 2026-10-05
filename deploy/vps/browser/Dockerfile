# UBAG live-browser (VPS profile) — headed Google Chrome on Xvfb, streamed to
# the operator dashboard by tools/live-browser/bridge.mjs (attach-only), with a
# Docker-private CDP proxy the gateway's live worker attaches through.
#
# This reuses the deploy/small/browser-viewer Chrome recipe (branded Google
# Chrome on a virtual display, stealth + keychain flags, a supervising
# watchdog) and exposes both the bridge's JPEG screencast and an alternate
# noVNC client on the same display. Both viewers require ingress Basic Auth.
# A human — never UBAG — completes the one-time provider
# sign-in; the profile persists on a mounted volume so it survives restarts.
#
# Google ships Linux Chrome for amd64 only; this VPS is x86_64.

# Pull a known-good Node 22 binary (bridge.mjs needs Node 22+ for global
# WebSocket) without adding a second apt repo to the Chrome image.
FROM node:22-bookworm-slim AS node

# trivy:ignore:DS-0002 — headed Chrome + Xvfb require root in this container (CDP attach only; no untrusted input executes here)
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive

# X virtual framebuffer + window manager + the tooling the entrypoint/watchdog
# use. socat exposes Chrome's loopback-bound CDP to other containers. gnupg
# dearmors Google's signing key.
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    xvfb \
    x11vnc \
    novnc \
    websockify \
    x11-utils \
    fluxbox \
    xdotool \
    wmctrl \
    socat \
    procps \
    ca-certificates \
    gnupg \
    tini \
    wget \
    pulseaudio \
    pulseaudio-utils \
    libopus0 \
    python3 \
  && rm -rf /var/lib/apt/lists/*

# Branded Google Chrome from Google's signed apt repo (the plain Debian
# `chromium` package is blocked as "not secure" at Google sign-in). amd64 only.
RUN install -d -m 0755 /etc/apt/keyrings \
  && wget -qO- https://dl.google.com/linux/linux_signing_key.pub \
       | gpg --dearmor -o /etc/apt/keyrings/google-chrome.gpg \
  && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" \
       > /etc/apt/sources.list.d/google-chrome.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends \
    google-chrome-stable \
    fonts-liberation \
  && rm -rf /var/lib/apt/lists/*

# Node 22 runtime for the bridge.
COPY --from=node /usr/local/bin/node /usr/local/bin/node

# The bridge is a zero-dependency single file — copy it directly.
COPY tools/live-browser/bridge.mjs /app/bridge.mjs
COPY deploy/vps/browser/entrypoint.sh /usr/local/bin/ubag-vps-browser
RUN chmod +x /usr/local/bin/ubag-vps-browser

# Voice media plane (browser-container side): the relay moves framed Opus
# between the gateway's MediaHub and PulseAudio's virtual mic / sink monitor
# (decode/encode via libopus through opus_bridge.py, apt-only deps). Inert
# unless the entrypoint's UBAG_VOICE_AUDIO_ENABLED=1 turns PulseAudio + the
# relay on; the port stays on the internal Docker network only.
COPY deploy/vps/browser/audio-relay.py /app/audio-relay.py
COPY deploy/vps/browser/opus_bridge.py /app/opus_bridge.py

# 58090 = dashboard-facing bridge WebSocket; 9223 = Docker-private CDP proxy the
# live worker attaches through; 9099 = voice audio relay (the gateway MediaHub
# dials it privately per active voice session). None are published to the
# host — the bridge WS reaches the browser only via nginx, CDP and the relay
# stay on the internal network.
EXPOSE 58090 9223 6080 9099

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/ubag-vps-browser"]
