# UBAG live-browser viewer (ToS-safe manual-login surface).
#
# Runs a real, persistent **Google Chrome** on a virtual display (Xvfb) and
# streams it to the operator's browser over noVNC (x11vnc + websockify). A human
# — never the machine — completes login, CAPTCHA, 2FA in this session. The worker
# attaches to the same Chrome over CDP (--remote-debugging-port) so live jobs
# reuse the user-owned, already-logged-in profile.
#
# Branded Google Chrome (not the Debian `chromium` package) is deliberate: Google
# attests genuine Chrome at sign-in and blocks plain Chromium as "this browser or
# app may not be secure", which made the provider login un-saveable. Chrome is the
# same Blink engine + same CDP, so UBAG drives it identically.
#
# NOTE: Google ships Linux Chrome for amd64/x86_64 only. The browser-viewer host
# must be x86_64 (the small-profile VPS is).
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# Base tooling + the X/VNC stack (Chrome itself is added from Google's apt repo
# in the next layer). gnupg is needed to dearmor Google's signing key.
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    xvfb \
    x11-utils \
    x11vnc \
    fluxbox \
    novnc \
    socat \
    websockify \
    xdotool \
    wmctrl \
    procps \
    ca-certificates \
    gnupg \
    tini \
    wget \
  && rm -rf /var/lib/apt/lists/*

# Google Chrome stable from Google's official, signed apt repository. Pinned to a
# keyring (no apt-key) and amd64. `fonts-liberation` gives Chrome sane default
# text rendering; the rest of Chrome's runtime deps are resolved by apt. The
# legacy Debian `chromium` package is intentionally NOT installed.
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

# noVNC ships vnc.html; make it the default index so /novnc/ resolves cleanly.
RUN ln -sf /usr/share/novnc/vnc.html /usr/share/novnc/index.html

COPY deploy/small/browser-viewer/entrypoint.sh /usr/local/bin/ubag-browser-viewer
RUN chmod +x /usr/local/bin/ubag-browser-viewer

# Operator convenience: a launchable Chrome on the shared profile + a fluxbox
# right-click menu entry, so a human can (re)open the browser from the noVNC desktop.
COPY deploy/small/browser-viewer/open-chrome /usr/local/bin/open-chrome
RUN chmod +x /usr/local/bin/open-chrome
COPY deploy/small/browser-viewer/fluxbox-menu /root/.fluxbox/menu

# 6080 = noVNC (websockify), 9223 = private CDP proxy for the worker.
EXPOSE 6080 9223

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/ubag-browser-viewer"]
