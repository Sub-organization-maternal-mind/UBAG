# UBAG Helper Node image (ubag-helper + the warm Python worker + Chromium).
#
# INERT: nothing in this repo starts it. The external fleet manager pulls the
# image, applies the Docker limits and the firewall rules, mounts the TLS files
# and starts it per deploy/helper/workload-manifest.json. The image holds no
# secret: certificates, keys and the CA are files the manager mounts at run time.
#
# Build context is the repository root:
#   docker build -f deploy/helper/Dockerfile --build-arg UBAG_HELPER_VERSION=sha-<commit> .
# CI builds it only from the manually dispatched helper-image workflow.

FROM golang:1.26-alpine AS build

ARG UBAG_HELPER_VERSION=dev
WORKDIR /src/apps/gateway

# The gateway module replaces the proto module with a local path, so it must be
# in the build context before `go mod download` resolves dependencies.
COPY packages/proto/gen/go /src/packages/proto/gen/go
COPY apps/gateway/go.mod apps/gateway/go.sum ./
RUN go mod download

COPY apps/gateway ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/ubag/ubag/apps/gateway/internal/helper.Version=${UBAG_HELPER_VERSION}" \
    -o /out/ubag-helper ./cmd/ubag-helper

FROM python:3.12-slim

# Chromium comes from Playwright: it is the browser the worker launches, and the
# helper refuses to start without a local Chrome, so the one symlink below makes
# the start-up lookup and the worker agree on a single binary.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright

# apt-get upgrade applies Debian security updates the base tag has not picked up.
RUN apt-get update -qq \
  && apt-get upgrade -y -qq \
  && apt-get install -y -qq --no-install-recommends xvfb x11vnc novnc websockify \
  && rm -rf /var/lib/apt/lists/* \
  && pip3 install --no-cache-dir "playwright>=1.49" \
  && playwright install --with-deps chromium \
  && ln -s "$(python3 -c 'from playwright.sync_api import sync_playwright as s; p = s().start(); print(p.chromium.executable_path); p.stop()')" /usr/local/bin/chromium \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd -g 10001 ubag-helper \
  && useradd -r -u 10001 -g ubag-helper -d /var/lib/ubag-helper -s /usr/sbin/nologin ubag-helper \
  && mkdir -p /var/lib/ubag-helper/home /var/lib/ubag-helper/profiles /etc/ubag-helper/tls \
  && chown -R ubag-helper:ubag-helper /var/lib/ubag-helper

WORKDIR /app
COPY --from=build /out/ubag-helper /app/ubag-helper
COPY apps/worker /app/apps/worker
COPY adapters /app/adapters
COPY deploy/helper/entrypoint.sh /app/helper-entrypoint.sh
COPY deploy/helper/operator-login.py /app/operator-login.py

# Non-secret paths only. UBAG_HELPER_LISTEN, _NODE_ID, _PRIMARY_URI_SAN,
# _WORKLOAD_VERSION and _MAX_ATTEMPTS are per-node and come from the workload
# manifest; the certificate, key and CA are mounted read-only under /etc/ubag-helper/tls.
ENV HOME=/var/lib/ubag-helper/home \
    UBAG_HELPER_ADAPTERS_DIR=/app/adapters \
    UBAG_HELPER_CHROME_BIN=/usr/local/bin/chromium \
    UBAG_HELPER_PROFILE_ROOT=/var/lib/ubag-helper/profiles \
    UBAG_HELPER_WORKER_PYTHON=python3 \
    UBAG_HELPER_WORKER_SCRIPT=/app/apps/worker/run_worker_daemon.py

USER 10001:10001

# No EXPOSE and no default listen address: the helper binds exactly one
# WireGuard address (UBAG_HELPER_LISTEN) and refuses a wildcard.
ENTRYPOINT ["/bin/sh", "/app/helper-entrypoint.sh"]
