# UBAG — Caddy Standard Profile

This directory contains the production Caddy configuration for UBAG.
It requires a **custom `xcaddy` build** because the stock Caddy binary does not
ship the rate-limit or WAF modules.

---

## Custom Build

### Prerequisites

```bash
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
```

### Build command

```bash
xcaddy build \
  --with github.com/mholt/caddy-ratelimit \
  --with github.com/corazawaf/coraza-caddy/v2
```

This produces a `caddy` binary in the current directory that includes:

| Module | Purpose |
|--------|---------|
| `github.com/mholt/caddy-ratelimit` | Per-IP rate limiting on `/v1/*` (100 requests per 60 seconds per IP) |
| `github.com/corazawaf/coraza-caddy/v2` | Coraza WAF (OWASP Core Rule Set) — activated in Task 1.2 via `import coraza.conf` |

---

## Running

```bash
caddy run --config deploy/caddy/Caddyfile.standard
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `UBAG_DOMAIN` | `ubag.example.com` | Public FQDN Caddy acquires a TLS cert for |
| `UBAG_BACKEND_ADDR` | `gateway:8080` | Upstream gateway address for `/v1/*` reverse-proxy |

---

## Hot-reload (zero connection drop)

The standard Caddyfile binds the admin API to `localhost:2019` only (never
public). This lets you reload config in-place without dropping any live
connections:

```bash
# Reload from the file that caddy was started with
caddy reload --address localhost:2019

# Or POST the new config directly
curl -X POST http://localhost:2019/load \
  -H "Content-Type: text/caddyfile" \
  --data-binary @deploy/caddy/Caddyfile.standard
```

Both paths are safe because the admin socket is loopback-only. The running
process swaps routes atomically and keeps existing TLS sessions alive.

---

## Standard profile

The small Docker Compose profile now uses `deploy/small/nginx-dashboard` for
local dashboard/API/noVNC ingress. This Caddy directory tracks the production
standard profile: automatic HTTPS, HTTP/3, rate limiting, and WAF-ready config
for internet-facing deployments.

---

## Feature summary (standard profile)

- **TLS** — automatic via ACME (Let's Encrypt / ZeroSSL); `auto_https on`
- **HTTP/3 (QUIC)** — `protocols h1 h2 h3` in the global `servers` block
- **Rate limiting** — `caddy-ratelimit` zone `api_per_ip`: 100 events / 60 s per remote IP, applied to `/v1/*`
- **Compression** — `encode zstd gzip`
- **Security headers** — `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`, `X-XSS-Protection`, `Strict-Transport-Security`
- **Hidden internal routes** — `/v1/metrics*` and `/v1/ready*` respond 404 (not proxied)
- **Admin API** — bound to `localhost:2019` only (never public)
- **WAF placeholder** — `# import coraza.conf  # Task 1.2 — enable WAF` in the site block
