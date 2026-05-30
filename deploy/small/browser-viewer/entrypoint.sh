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
PROFILE_DIR="${UBAG_BROWSER_PROFILE_DIR:-/profiles/default}"
CDP_PORT="${UBAG_BROWSER_CDP_PORT:-9222}"
VNC_PORT="${UBAG_BROWSER_VNC_PORT:-5900}"
NOVNC_PORT="${UBAG_BROWSER_NOVNC_PORT:-6080}"
START_URL="${UBAG_BROWSER_START_URL:-about:blank}"

# A VNC password is mandatory — never expose an unauthenticated remote display.
if [ -z "${UBAG_BROWSER_VNC_PASSWORD:-}" ]; then
  echo "browser-viewer: UBAG_BROWSER_VNC_PASSWORD is required" >&2
  exit 1
fi

mkdir -p "$PROFILE_DIR" /run/ubag
x11vnc -storepasswd "$UBAG_BROWSER_VNC_PASSWORD" /run/ubag/vncpass >/dev/null 2>&1

cleanup() {
  # Best-effort teardown so a restarting container does not leak processes.
  kill 0 2>/dev/null || true
}
trap cleanup INT TERM EXIT

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
  --start-maximized \
  "$START_URL" &

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
