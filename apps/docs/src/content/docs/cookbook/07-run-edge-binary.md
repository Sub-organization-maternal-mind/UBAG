---
title: Run the Edge Binary
description: Deploy and run the UBAG edge binary for low-latency, on-premise browser automation.
---

The edge binary bundles the UBAG worker, sidecar, and adapter runtimes into a single
self-contained executable for deployment at the edge.

## Download

```bash
# macOS (arm64)
curl -L https://releases.ubag.io/edge/latest/ubag-edge-darwin-arm64 -o ubag-edge
chmod +x ubag-edge

# Linux (amd64)
curl -L https://releases.ubag.io/edge/latest/ubag-edge-linux-amd64 -o ubag-edge
chmod +x ubag-edge
```

## Configure

Create `ubag-edge.toml`:

```toml
[gateway]
url = "https://gateway.example.com"
app_secret = "${UBAG_APP_SECRET}"

[worker]
concurrency = 4
region = "us-east-1"

[browser]
headless = true
```

## Run

```bash
./ubag-edge --config ubag-edge.toml
```

## Verify connectivity

```bash
./ubag-edge status
# Edge binary v0.9.0 | connected to gateway.example.com | 4 worker slots
```

## Docker

```dockerfile
FROM ghcr.io/ubag/edge:latest
COPY ubag-edge.toml /etc/ubag/config.toml
ENTRYPOINT ["ubag-edge", "--config", "/etc/ubag/config.toml"]
```

```bash
docker run -e UBAG_APP_SECRET=$UBAG_APP_SECRET ghcr.io/ubag/edge:latest
```

See [Deployment Profiles](/deployment/profiles) for tier-specific configuration options.
