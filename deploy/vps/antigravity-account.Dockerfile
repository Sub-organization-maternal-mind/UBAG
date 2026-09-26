FROM python:3.12-slim

RUN apt-get update -qq \
  && apt-get install -y --no-install-recommends ca-certificates curl dbus-daemon gnome-keyring \
  && rm -rf /var/lib/apt/lists/* \
  && curl -fsSLo /tmp/antigravity-install.sh https://antigravity.google/cli/install.sh \
  && bash /tmp/antigravity-install.sh --dir /usr/local/bin \
  && rm /tmp/antigravity-install.sh \
  && agy --version \
  && groupadd -g 10001 agy-ipc \
  && useradd -r -u 10002 -g agy-ipc -d /var/lib/agy -m agy \
  && mkdir -p /run/agy /run/ubag-antigravity \
  && chown -R agy:agy-ipc /var/lib/agy /run/agy /run/ubag-antigravity \
  && chmod 0700 /var/lib/agy /run/agy \
  && chmod 0770 /run/ubag-antigravity

COPY adapters/antigravity_cli /app/adapters/antigravity_cli
COPY deploy/vps/antigravity-entrypoint.sh /usr/local/bin/antigravity-entrypoint.sh
RUN chmod 0755 /usr/local/bin/antigravity-entrypoint.sh

ENV HOME=/var/lib/agy \
  XDG_RUNTIME_DIR=/run/agy \
  DBUS_SESSION_BUS_ADDRESS=unix:path=/run/agy/bus \
  PYTHONPATH=/app/adapters/antigravity_cli \
  PYTHONDONTWRITEBYTECODE=1 \
  AGY_BINARY=/usr/local/bin/agy \
  UBAG_ANTIGRAVITY_SOCKET_DIR=/run/ubag-antigravity

USER agy
ENTRYPOINT ["/usr/local/bin/antigravity-entrypoint.sh"]