FROM golang:1.26-alpine AS build

WORKDIR /src/apps/gateway

# The gateway module replaces github.com/ubag/ubag/packages/proto/gen/go with a
# local path (../../packages/proto/gen/go), so the replaced module must be
# present in the build context before `go mod download` resolves dependencies.
COPY packages/proto/gen/go /src/packages/proto/gen/go
COPY apps/gateway/go.mod apps/gateway/go.sum ./
RUN go mod download

COPY apps/gateway ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ubag-gateway ./cmd/gateway

FROM python:3.12-slim

ARG UBAG_ANTIGRAVITY_SLOT_1_ID=acct_1
ARG UBAG_ANTIGRAVITY_SLOT_2_ID=acct_2
ARG UBAG_ANTIGRAVITY_SLOT_3_ID=acct_3

# apt-get upgrade applies Debian security updates to the base image packages
# (e.g. libpcre2 CVE-2026-103111, fixed upstream but not yet reflected in the
# python:3.12-slim tag); Trivy fails the build on fixed HIGH/CRITICAL findings.
RUN apt-get update -qq \
  && apt-get upgrade -y -qq \
  && apt-get install -y --no-install-recommends wget postgresql-client \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd -r ubag \
  && groupadd -g 10001 agy-ipc \
  && useradd -r -g ubag ubag \
  && usermod -a -G agy-ipc ubag \
  && pip3 install --no-cache-dir "playwright>=1.49" \
  && mkdir -p /var/lib/ubag/executor-spool /var/lib/ubag/chat-ledger /var/lib/ubag/antigravity /run/ubag-antigravity \
  && chown -R ubag:ubag /var/lib/ubag \
  && chown ubag:agy-ipc /run/ubag-antigravity \
  && chmod 0770 /run/ubag-antigravity \
  && for slot in "$UBAG_ANTIGRAVITY_SLOT_1_ID" "$UBAG_ANTIGRAVITY_SLOT_2_ID" "$UBAG_ANTIGRAVITY_SLOT_3_ID"; do \
    echo "$slot" | grep -Eq '^acct_[0-9]+$' || exit 1; \
    mkdir -p "/run/ubag-antigravity/$slot"; \
    chown ubag:agy-ipc "/run/ubag-antigravity/$slot"; \
    chmod 0770 "/run/ubag-antigravity/$slot"; \
  done

WORKDIR /app

COPY --from=build /out/ubag-gateway /app/ubag-gateway
COPY apps/worker /app/apps/worker
ARG UBAG_WITH_ANTIGRAVITY_SDK=false
RUN if [ "$UBAG_WITH_ANTIGRAVITY_SDK" = "true" ]; then pip3 install --no-cache-dir '/app/apps/worker[antigravity-sdk]'; fi
COPY adapters /app/adapters
# postgresql-client (psql) + these SQL files aren't needed by docker-compose.small.yml
# (its own postgres-migrate service applies them via a host bind-mount instead), but
# platforms without volume mounts or a pre-deploy hook — e.g. Render's Free plan,
# which supports neither — need them baked into the image so gateway-entrypoint.sh
# can apply them itself before the gateway binary starts.
COPY migrations/postgres /app/migrations/postgres
COPY deploy/small/gateway-entrypoint.sh /app/gateway-entrypoint.sh
RUN chmod +x /app/gateway-entrypoint.sh

USER ubag

EXPOSE 8080
ENTRYPOINT ["/app/gateway-entrypoint.sh"]
