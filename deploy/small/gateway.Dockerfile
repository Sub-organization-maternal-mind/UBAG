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

FROM alpine:3.20

RUN addgroup -S ubag \
  && adduser -S -G ubag ubag \
  && apk add --no-cache ca-certificates python3 py3-pip wget \
  && pip3 install --no-cache-dir --break-system-packages "playwright>=1.49" "patchright>=1.49" \
  && mkdir -p /var/lib/ubag/executor-spool \
  && chown -R ubag:ubag /var/lib/ubag

WORKDIR /app

COPY --from=build /out/ubag-gateway /app/ubag-gateway
COPY apps/worker /app/apps/worker
COPY adapters /app/adapters

USER ubag

EXPOSE 8080
ENTRYPOINT ["/app/ubag-gateway"]
