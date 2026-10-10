#!/bin/sh
set -eu
export DISPLAY=:99
Xvfb "$DISPLAY" -screen 0 1440x900x24 -nolisten tcp -ac >/tmp/xvfb.log 2>&1 &
for attempt in 1 2 3 4 5; do
    [ -S /tmp/.X11-unix/X99 ] && break
    sleep 1
done
[ -S /tmp/.X11-unix/X99 ] || exit 1
# Operator access is loopback only: reach it with an authenticated SSH tunnel.
# No public browser/CDP listener and no credentials are supplied by this image.
x11vnc -display "$DISPLAY" -localhost -rfbport 5901 -forever -shared -nopw >/tmp/x11vnc.log 2>&1 &
websockify --web=/usr/share/novnc 127.0.0.1:6081 127.0.0.1:5901 >/tmp/novnc.log 2>&1 &
exec /app/ubag-helper
