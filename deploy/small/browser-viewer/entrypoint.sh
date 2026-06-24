#!/bin/sh
# UBAG live-browser viewer entrypoint.
#
# Boots a virtual display, a persistent Chromium with CDP enabled, and exposes
# the display over noVNC. A human operator uses noVNC to log in manually; the
# worker attaches to the same Chromium over CDP. UBAG never fills credentials,
# captures cookies/storage-state, or solves CAPTCHAs.
#
# This file MUST use LF line endings — CRLF breaks `sh` inside the container.
set -eu

DISPLAY_NUM="${UBAG_BROWSER_DISPLAY:-:99}"
SCREEN_GEOMETRY="${UBAG_BROWSER_GEOMETRY:-1280x800x24}"
# Parsed screen width/height (added) — used to keep Chromium full-screen.
SCREEN_W="${SCREEN_GEOMETRY%%x*}"
SCREEN_REST="${SCREEN_GEOMETRY#*x}"
SCREEN_H="${SCREEN_REST%%x*}"
PROFILE_DIR="${UBAG_BROWSER_PROFILE_DIR:-/profiles/default}"
CDP_PORT="${UBAG_BROWSER_CDP_PORT:-9222}"
CDP_PROXY_PORT="${UBAG_BROWSER_CDP_PROXY_PORT:-9223}"
VNC_PORT="${UBAG_BROWSER_VNC_PORT:-5900}"
NOVNC_PORT="${UBAG_BROWSER_NOVNC_PORT:-6080}"
START_URL="${UBAG_BROWSER_START_URL:-about:blank}"

# A VNC password is mandatory — never expose an unauthenticated remote display.
if [ -z "${UBAG_BROWSER_VNC_PASSWORD:-}" ]; then
  echo "browser-viewer: UBAG_BROWSER_VNC_PASSWORD is required" >&2
  exit 1
fi

mkdir -p "$PROFILE_DIR" /run/ubag /root/.fluxbox
x11vnc -storepasswd "$UBAG_BROWSER_VNC_PASSWORD" /run/ubag/vncpass >/dev/null 2>&1

cleanup() {
  trap - INT TERM EXIT
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup INT TERM

DISPLAY_ID="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${DISPLAY_ID}-lock" "/tmp/.X11-unix/X${DISPLAY_ID}"
rm -f "$PROFILE_DIR"/SingletonLock "$PROFILE_DIR"/SingletonSocket "$PROFILE_DIR"/SingletonCookie

# Virtual framebuffer (no TCP listener — display is local to the container).
Xvfb "$DISPLAY_NUM" -screen 0 "$SCREEN_GEOMETRY" -nolisten tcp &
export DISPLAY="$DISPLAY_NUM"

# Wait for the display to be ready before launching the WM/browser.
tries=0
while [ "$tries" -lt 50 ]; do
  if xdpyinfo -display "$DISPLAY_NUM" >/dev/null 2>&1; then
    break
  fi
  tries=$((tries + 1))
  sleep 0.2
done

# Lightweight window manager so dialogs, focus, and inputs behave normally.
fluxbox >/dev/null 2>&1 &

# Persistent Chromium with CDP for the worker to attach to. The profile lives on
# a mounted volume so manual logins survive restarts (user-owned session reuse).
chromium \
  --no-sandbox \
  --user-data-dir="$PROFILE_DIR" \
  --remote-debugging-address=0.0.0.0 \
  --remote-debugging-port="$CDP_PORT" \
  --no-first-run \
  --no-default-browser-check \
  --disable-dev-shm-usage \
  --disable-gpu \
  --window-position=0,0 \
  --window-size="$SCREEN_W,$SCREEN_H" \
  --start-maximized \
  "$START_URL" &

# Defeat the fluxbox/Chromium startup race that can otherwise leave the window at
# Chromium's 200x200 default: force every Chromium window to fill the screen once
# it maps. One-shot (first ~20s of boot) so it never fights the worker later.
( set +e
  i=0
  while [ "$i" -lt 20 ]; do
    if xdotool search --class chromium >/dev/null 2>&1; then
      for w in $(xdotool search --class chromium 2>/dev/null); do
        xdotool windowsize "$w" "$SCREEN_W" "$SCREEN_H" 2>/dev/null
        xdotool windowmove "$w" 0 0 2>/dev/null
      done
      break
    fi
    i=$((i + 1))
    sleep 1
  done ) &

# Some Chromium builds keep DevTools on loopback even when
# --remote-debugging-address is set. Keep the browser listener private and
# expose a Docker-private proxy for the gateway/worker to attach through.
socat \
  "TCP-LISTEN:$CDP_PROXY_PORT,fork,reuseaddr,bind=0.0.0.0" \
  "TCP:127.0.0.1:$CDP_PORT" &

# Share the display over VNC on loopback only; websockify wraps it for noVNC.
x11vnc \
  -display "$DISPLAY_NUM" \
  -rfbport "$VNC_PORT" \
  -rfbauth /run/ubag/vncpass \
  -forever \
  -shared \
  -localhost \
  -bg

# noVNC web client + websocket bridge. This is the only externally reachable
# surface and it is still gated by the VNC password above.
exec websockify --web=/usr/share/novnc "$NOVNC_PORT" "localhost:$VNC_PORT"
