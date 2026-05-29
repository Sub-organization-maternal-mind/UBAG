# UBAG Sidecar

The sidecar is a loopback-only localhost bridge for legacy desktop apps and
scripts that cannot use the full SDK directly.

Run locally:

```powershell
pnpm --filter @ubag/sidecar build
node packages/sidecar/dist/index.js --host 127.0.0.1 --port 7878 --gateway http://127.0.0.1:8080
```

Endpoints:

- `GET /health`: sidecar health and configured gateway target.
- `/v1/*`: transparent proxy to the configured gateway.

The process refuses non-loopback binding unless `--allow-non-loopback` is passed
explicitly after a firewall review.
