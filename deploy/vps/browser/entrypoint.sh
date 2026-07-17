#!/bin/sh
# UBAG live-browser (VPS) entrypoint.
#
# Boots a virtual display and a SUPERVISED persistent Google Chrome with CDP,
# then runs tools/live-browser/bridge.mjs in ATTACH-ONLY mode to stream that
# Chrome to the operator dashboard and accept mouse/keyboard back. A human logs
# in to the AI providers manually; the gateway's live worker attaches to the
# same Chrome over CDP (via the socat proxy) and reuses the logged-in session.
# UBAG never types credentials, captures cookies, or solves CAPTCHAs.
#
# Chrome ownership: the WATCHDOG here owns Chrome's lifecycle (correct
# container-safe flags + relaunch-on-death). The bridge is ATTACH-ONLY
# (UBAG_LIVE_BROWSER_ATTACH_ONLY=1) so it never launches its own Chrome with the
# desktop/loopback flags that would fail as root in a container — it only
# attaches to the CDP endpoint this script keeps alive.
#
# MUST use LF line endings — CRLF breaks `sh` in the container.
set -eu

DISPLAY_NUM="${UBAG_BROWSER_DISPLAY:-:99}"
SCREEN_GEOMETRY="${UBAG_BROWSER_GEOMETRY:-1280x800x24}"
SCREEN_W="${SCREEN_GEOMETRY%%x*}"
SCREEN_REST="${SCREEN_GEOMETRY#*x}"
SCREEN_H="${SCREEN_REST%%x*}"
PROFILE_DIR="${UBAG_BROWSER_PROFILE_DIR:-/profiles/default}"
# Chrome's CDP port (loopback inside the container). Matches the bridge's
# UBAG_LIVE_BROWSER_CDP_PORT default (58091) so the bridge attaches with no
# extra config.
CDP_PORT="${UBAG_BROWSER_CDP_PORT:-58091}"
# Docker-private proxy port the gateway's live worker attaches through.
CDP_PROXY_PORT="${UBAG_BROWSER_CDP_PROXY_PORT:-9223}"
# Dashboard-facing bridge WebSocket port.
BRIDGE_PORT="${UBAG_LIVE_BROWSER_PORT:-58090}"
START_URL="${UBAG_BROWSER_START_URL:-https://chatgpt.com}"
WATCHDOG_INTERVAL="${UBAG_BROWSER_WATCHDOG_INTERVAL:-3}"
CDP_GRACE_PROBES="${UBAG_BROWSER_CDP_GRACE_PROBES:-12}"

mkdir -p "$PROFILE_DIR" /run/ubag /root/.fluxbox

cleanup() {
  trap - INT TERM EXIT
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup INT TERM

DISPLAY_ID="${DISPLAY_NUM#:}"
rm -f "/tmp/.X${DISPLAY_ID}-lock" "/tmp/.X11-unix/X${DISPLAY_ID}"

# Virtual framebuffer (no TCP listener — display is container-local).
Xvfb "$DISPLAY_NUM" -screen 0 "$SCREEN_GEOMETRY" -nolisten tcp &
export DISPLAY="$DISPLAY_NUM"

tries=0
while [ "$tries" -lt 50 ]; do
  if xdpyinfo -display "$DISPLAY_NUM" >/dev/null 2>&1; then
    break
  fi
  tries=$((tries + 1))
  sleep 0.2
done

# Lightweight WM so dialogs, focus, and input behave normally.
fluxbox >/dev/null 2>&1 &

# Clear stale profile guards + crash-restore state WITHOUT touching cookies or
# "Login Data" (separate SQLite files) — the user-owned login survives relaunch.
reset_profile_guards() {
  rm -f "$PROFILE_DIR"/SingletonLock "$PROFILE_DIR"/SingletonSocket "$PROFILE_DIR"/SingletonCookie 2>/dev/null || true
  for prefs in "$PROFILE_DIR"/Default/Preferences "$PROFILE_DIR"/Preferences; do
    if [ -f "$prefs" ]; then
      sed -i 's/"exit_type":"[^"]*"/"exit_type":"Normal"/g; s/"exited_cleanly":false/"exited_cleanly":true/g' "$prefs" 2>/dev/null || true
    fi
  done
}

# Persistent headed Chrome with CDP. Flags mirror the proven browser-viewer
# recipe: they make the browser undetectable to the PROVIDER (so manual sign-in
# is not blocked as "not secure") and let the worker attach over CDP — they
# never restrict what UBAG drives. CDP binds loopback; socat exposes it to the
# worker's container privately.
CHROME_PID=""
start_chrome() {
  reset_profile_guards
  google-chrome-stable \
    --no-sandbox \
    --user-data-dir="$PROFILE_DIR" \
    --remote-debugging-address=127.0.0.1 \
    --remote-debugging-port="$CDP_PORT" \
    --remote-allow-origins=* \
    --disable-blink-features=AutomationControlled \
    --password-store=basic \
    --use-mock-keychain \
    --no-first-run \
    --no-default-browser-check \
    --hide-crash-restore-bubble \
    --disable-dev-shm-usage \
    --disable-gpu \
    --disable-infobars \
    --disable-features=Translate,OptimizationHints,InterestFeedContentSuggestions,CalculateNativeWinOcclusion \
    --disable-backgrounding-occluded-windows \
    --disable-renderer-backgrounding \
    --disable-background-timer-throttling \
    --window-position=0,0 \
    --window-size="$SCREEN_W,$SCREEN_H" \
    --start-maximized \
    "$START_URL" >/run/ubag/chrome.log 2>&1 &
  CHROME_PID=$!
}

maximize_chrome() {
  i=0
  while [ "$i" -lt 20 ]; do
    if xdotool search --class chrome >/dev/null 2>&1; then
      for w in $(xdotool search --class chrome 2>/dev/null); do
        xdotool windowsize "$w" "$SCREEN_W" "$SCREEN_H" 2>/dev/null || true
        xdotool windowmove "$w" 0 0 2>/dev/null || true
      done
      break
    fi
    i=$((i + 1))
    sleep 1
  done
}

cdp_alive() {
  wget -qO- "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1
}

start_chrome
maximize_chrome &

# Expose Chrome's loopback CDP to the worker's container over the private
# network. (Chrome may keep DevTools on loopback even with
# --remote-debugging-address=0.0.0.0, so proxy it explicitly.)
socat "TCP-LISTEN:$CDP_PROXY_PORT,fork,reuseaddr,bind=0.0.0.0" "TCP:127.0.0.1:$CDP_PORT" &

# Wait for Chrome's CDP before starting the bridge (attach-only bridge would
# otherwise poll, but this keeps startup logs clean).
tries=0
while [ "$tries" -lt 60 ]; do
  if cdp_alive; then break; fi
  tries=$((tries + 1))
  sleep 0.5
done

# The streaming bridge — attach-only, bound to all interfaces so nginx (other
# container) can proxy it. It attaches to the Chrome above and never launches
# its own. Wrapped in a restart loop: the bridge self-heals from CDP/page churn
# in-process (unhandledRejection/uncaughtException -> re-attach), but if it ever
# hard-exits anyway, this brings it back so the operator's live view is never
# permanently dead (the Chrome watchdog below doesn't supervise the bridge).
(
  while true; do
    UBAG_LIVE_BROWSER_ATTACH_ONLY=1 \
    UBAG_LIVE_BROWSER_BIND=0.0.0.0 \
    UBAG_LIVE_BROWSER_PORT="$BRIDGE_PORT" \
    UBAG_LIVE_BROWSER_CDP_PORT="$CDP_PORT" \
    UBAG_LIVE_BROWSER_PROFILE="$PROFILE_DIR" \
    UBAG_LIVE_BROWSER_START_URL="$START_URL" \
      node /app/bridge.mjs >>/run/ubag/bridge.log 2>&1
    echo "$(date -u +%FT%TZ) vps-browser: bridge exited; restarting in 2s" >>/run/ubag/bridge.log
    sleep 2
  done
) &

# ---------------------------------------------------------------------------
# Chrome watchdog (foreground = the container's main process). Relaunches Chrome
# on crash/close on the SAME profile; the attach-only bridge re-attaches itself.
# ---------------------------------------------------------------------------
restart_chrome() {
  echo "vps-browser: chrome unhealthy ($1); restarting on profile $PROFILE_DIR" >&2
  if [ -n "$CHROME_PID" ]; then
    kill "$CHROME_PID" 2>/dev/null || true
    wait "$CHROME_PID" 2>/dev/null || true
  fi
  pkill -9 -x chrome 2>/dev/null || true
  sleep 1
  start_chrome
  maximize_chrome &
}

cdp_fail=0
while true; do
  sleep "$WATCHDOG_INTERVAL"
  if [ -z "$CHROME_PID" ] || ! kill -0 "$CHROME_PID" 2>/dev/null; then
    restart_chrome "process exited"
    cdp_fail=0
    continue
  fi
  if cdp_alive; then
    cdp_fail=0
    continue
  fi
  cdp_fail=$((cdp_fail + 1))
  if [ "$cdp_fail" -ge "$CDP_GRACE_PROBES" ]; then
    restart_chrome "DevTools unresponsive for ${cdp_fail} probes"
    cdp_fail=0
  fi
done
