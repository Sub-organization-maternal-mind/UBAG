FROM golang:1.25-alpine AS build

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

RUN apt-get update -qq && apt-get install -y --no-install-recommends wget \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd -r ubag \
  && useradd -r -g ubag ubag \
  && pip3 install --no-cache-dir "playwright>=1.49" "patchright>=1.49" \
  && mkdir -p /var/lib/ubag/executor-spool \
  && chown -R ubag:ubag /var/lib/ubag

WORKDIR /app

COPY --from=build /out/ubag-gateway /app/ubag-gateway
COPY apps/worker /app/apps/worker
COPY adapters /app/adapters

USER ubag

EXPOSE 8080
ENTRYPOINT ["/app/ubag-gateway"]
