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
| `github.com/mholt/caddy-ratelimit` | Per-IP token-bucket rate limiting on `/v1/*` (100 req / 60 s, burst 20) |
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

## Profile switch (`UBAG_CADDY_PROFILE`)

UBAG supports two Caddy profiles selected by the `UBAG_CADDY_PROFILE` environment
variable (consumed by Docker Compose / Helm):

| Value | Config file | Use case |
|-------|------------|---------|
| `small` *(default for local dev)* | `deploy/small/caddy/Caddyfile` | Loopback, `auto_https off`, plain HTTP `:80` |
| `standard` | `deploy/caddy/Caddyfile.standard` | Production, `auto_https on`, HTTP/3, real TLS |

The `small` profile intentionally disables TLS and rate limiting so the stack
can run on a laptop without a public domain name.  The `standard` profile is the
baseline for any internet-facing deployment.

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
