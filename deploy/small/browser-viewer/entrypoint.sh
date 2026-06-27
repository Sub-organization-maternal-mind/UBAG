#!/bin/sh
# UBAG live-browser viewer entrypoint.
#
# Boots a virtual display, a SUPERVISED persistent Chrome with CDP enabled, and
# exposes the display over noVNC. A human operator uses noVNC to log in manually;
# the worker attaches to the same Chrome over CDP. UBAG never fills credentials,
# captures cookies/storage-state, or solves CAPTCHAs.
#
# Resilience / autonomy (the operator-reported failures this fixes):
#   * "the browsers close and relaunch whenever they desire" — Chrome now runs
#     under a foreground WATCHDOG. If it crashes, OOMs, wedges, or the operator
#     closes the last window, stale Singleton locks + crash-restore state are
#     cleared and it is relaunched ON THE SAME profile, so the user-owned login
#     session is continuously available instead of staying dead until a human
#     re-opens it. (Previously Chrome was a fire-and-forget background process
#     and the container healthcheck only probed noVNC, so a dead browser was
#     never noticed or restarted.)
#   * "cannot login to my Google account / cannot save the auth sessions" —
#     Chrome is launched with STEALTH flags that hide the browser-automation
#     fingerprint FROM THE PROVIDER (never from UBAG) so your one-time, manual
#     Google / DeepSeek / ChatGPT / Perplexity sign-in is not blocked by "this
#     browser or app may not be secure". UBAG keeps FULL CDP control and
#     automates everything after that single login; the profile (cookies/login)
#     persists on the mounted volume across restarts. UBAG never types creds.
#
# Automation policy: the ONLY manual step is logging in to the AI providers.
# Everything else — launching, attaching, prompting, reading, recovering — is
# automated. The flags below MAKE that possible; they do not restrict UBAG.
#
# This file MUST use LF line endings — CRLF breaks `sh` inside the container.
set -eu

DISPLAY_NUM="${UBAG_BROWSER_DISPLAY:-:99}"
SCREEN_GEOMETRY="${UBAG_BROWSER_GEOMETRY:-1280x800x24}"
# Parsed screen width/height — used to keep Chrome full-screen.
SCREEN_W="${SCREEN_GEOMETRY%%x*}"
SCREEN_REST="${SCREEN_GEOMETRY#*x}"
SCREEN_H="${SCREEN_REST%%x*}"
PROFILE_DIR="${UBAG_BROWSER_PROFILE_DIR:-/profiles/default}"
CDP_PORT="${UBAG_BROWSER_CDP_PORT:-9222}"
CDP_PROXY_PORT="${UBAG_BROWSER_CDP_PROXY_PORT:-9223}"
VNC_PORT="${UBAG_BROWSER_VNC_PORT:-5900}"
NOVNC_PORT="${UBAG_BROWSER_NOVNC_PORT:-6080}"
START_URL="${UBAG_BROWSER_START_URL:-about:blank}"
# Watchdog cadence (seconds) and how long Chrome's DevTools may be unresponsive
# while the process is still alive before we force a restart (~ probes * cadence).
WATCHDOG_INTERVAL="${UBAG_BROWSER_WATCHDOG_INTERVAL:-3}"
CDP_GRACE_PROBES="${UBAG_BROWSER_CDP_GRACE_PROBES:-12}"

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

# Clear stale profile guards + crash-restore state WITHOUT touching cookies/login.
# Removing only the Singleton* files lets a fresh Chrome reclaim a profile left
# behind by a crashed/killed predecessor; normalizing exit_type/exited_cleanly
# suppresses the "Chrome didn't shut down correctly / Restore pages?" interstitial
# that would otherwise sit in front of the provider UI after an unclean exit. The
# Cookies / "Login Data" stores are separate SQLite files and are never touched,
# so the user-owned login survives every relaunch.
reset_profile_guards() {
  rm -f "$PROFILE_DIR"/SingletonLock "$PROFILE_DIR"/SingletonSocket "$PROFILE_DIR"/SingletonCookie 2>/dev/null || true
  for prefs in "$PROFILE_DIR"/Default/Preferences "$PROFILE_DIR"/Preferences; do
    if [ -f "$prefs" ]; then
      sed -i 's/"exit_type":"[^"]*"/"exit_type":"Normal"/g; s/"exited_cleanly":false/"exited_cleanly":true/g' "$prefs" 2>/dev/null || true
    fi
  done
}

# Persistent Chrome with CDP for the worker to attach to. The profile lives on a
# mounted volume so manual logins survive restarts (user-owned session reuse).
#
# Flag rationale (these ENABLE end-to-end automation by making the bot
# undetectable to the provider and letting the worker attach — they never limit
# what UBAG can drive):
#   --disable-blink-features=AutomationControlled  drops navigator.webdriver, the
#       primary signal Google/most providers use to block sign-in on "automated"
#       browsers ("this browser or app may not be secure"). Hidden from the
#       provider only; UBAG still fully controls the browser over CDP.
#   --remote-allow-origins=*  modern Chrome (>=111) rejects CDP attaches whose
#       Origin is not allow-listed; the worker's connect_over_cdp needs this.
#   --password-store=basic --use-mock-keychain  avoid the gnome-keyring prompt
#       that can stall sign-in and lose the saved session in a headless container.
CHROMIUM_PID=""
start_chromium() {
  reset_profile_guards
  # Branded Google Chrome (not Chrome): Google trusts genuine Chrome at sign-in
  # and blocks plain Chrome as "less secure". Same Blink engine + same CDP, so
  # UBAG drives it identically.
  google-chrome-stable \
    --no-sandbox \
    --user-data-dir="$PROFILE_DIR" \
    --remote-debugging-address=0.0.0.0 \
    --remote-debugging-port="$CDP_PORT" \
    --remote-allow-origins=* \
    --disable-blink-features=AutomationControlled \
    --password-store=basic \
    --use-mock-keychain \
    --no-first-run \
    --no-default-browser-check \
    --disable-dev-shm-usage \
    --disable-gpu \
    --disable-infobars \
    --disable-features=Translate,OptimizationHints,InterestFeedContentSuggestions \
    --window-position=0,0 \
    --window-size="$SCREEN_W,$SCREEN_H" \
    --start-maximized \
    "$START_URL" >/run/ubag/chrome.log 2>&1 &
  CHROMIUM_PID=$!
}

# Force the Chrome window full-screen once it maps (defeats the fluxbox/Chrome
# startup race that can otherwise leave the window at Chrome's tiny default).
maximize_chromium() {
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

start_chromium
maximize_chromium &

# Some Chrome builds keep DevTools on loopback even when
# --remote-debugging-address is set. Keep the browser listener private and expose
# a Docker-private proxy for the gateway/worker to attach through.
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

# noVNC web client + websocket bridge. Backgrounded (previously the foreground
# `exec`) so the watchdog below owns the foreground and keeps Chrome alive.
websockify --web=/usr/share/novnc "$NOVNC_PORT" "localhost:$VNC_PORT" &

# ---------------------------------------------------------------------------
# Chrome watchdog (foreground = the container's main process).
# ---------------------------------------------------------------------------
# True when Chrome's DevTools endpoint answers — the real "the worker can
# attach" signal (a bare process check can be true while DevTools is wedged).
cdp_alive() {
  wget -qO- "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1
}

restart_chromium() {
  echo "browser-viewer: chromium unhealthy ($1); restarting on profile $PROFILE_DIR" >&2
  if [ -n "$CHROMIUM_PID" ]; then
    kill "$CHROMIUM_PID" 2>/dev/null || true
    wait "$CHROMIUM_PID" 2>/dev/null || true
  fi
  # Sweep any orphaned Chrome processes (renderers / a non-exec wrapper child)
  # that could still hold the profile's Singleton lock and make the relaunch
  # attach to a half-dead instance. start_chromium's reset_profile_guards then
  # clears the lock files before the fresh launch.
  pkill -9 -x chrome 2>/dev/null || true
  sleep 1
  start_chromium
  maximize_chromium &
}

cdp_fail=0
while true; do
  sleep "$WATCHDOG_INTERVAL"
  # Dead process -> relaunch immediately.
  if [ -z "$CHROMIUM_PID" ] || ! kill -0 "$CHROMIUM_PID" 2>/dev/null; then
    restart_chromium "process exited"
    cdp_fail=0
    continue
  fi
  # Alive process: tolerate a slow/transient DevTools window (cold first boot
  # initializes the profile) before forcing a restart.
  if cdp_alive; then
    cdp_fail=0
    continue
  fi
  cdp_fail=$((cdp_fail + 1))
  if [ "$cdp_fail" -ge "$CDP_GRACE_PROBES" ]; then
    restart_chromium "DevTools unresponsive for ${cdp_fail} probes"
    cdp_fail=0
  fi
done
