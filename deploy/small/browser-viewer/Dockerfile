# UBAG live-browser viewer (ToS-safe manual-login surface).
#
# Runs a real, persistent Chromium on a virtual display (Xvfb) and streams it to
# the operator's browser over noVNC (x11vnc + websockify). A human — never the
# machine — completes login, CAPTCHA, 2FA, and consent prompts in this session.
# The worker attaches to the same Chromium over CDP (--remote-debugging-port) so
# live jobs reuse the user-owned, already-logged-in profile.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    chromium \
    xvfb \
    x11-utils \
    x11vnc \
    fluxbox \
    novnc \
    socat \
    websockify \
    xdotool \
    wmctrl \
    ca-certificates \
    tini \
    wget \
  && rm -rf /var/lib/apt/lists/*

# noVNC ships vnc.html; make it the default index so /novnc/ resolves cleanly.
RUN ln -sf /usr/share/novnc/vnc.html /usr/share/novnc/index.html

COPY deploy/small/browser-viewer/entrypoint.sh /usr/local/bin/ubag-browser-viewer
RUN chmod +x /usr/local/bin/ubag-browser-viewer

# Operator convenience: a launchable Chromium on the shared profile + a fluxbox
# right-click menu entry, so a human can (re)open the browser from the noVNC desktop.
COPY deploy/small/browser-viewer/open-chromium /usr/local/bin/open-chromium
RUN chmod +x /usr/local/bin/open-chromium
COPY deploy/small/browser-viewer/fluxbox-menu /root/.fluxbox/menu

# 6080 = noVNC (websockify), 9223 = private CDP proxy for the worker.
EXPOSE 6080 9223

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/ubag-browser-viewer"]
