# UBAG mTLS

Mutual-TLS material and configuration for high-assurance UBAG deployments
(Tier 3+ per the blueprint: "mTLS for non-loopback bindings" and
"High-assurance server clients"). **No private keys are committed** — they are
generated at runtime and gitignored.

## Files

```
deploy/mtls/
├── gen-certs.sh                       # generate dev CA + server + client certs (Linux/macOS)
├── gen-certs.ps1                      # same, for Windows (needs openssl)
├── caddy/Caddyfile.mtls.example       # Caddy edge requiring client certs
├── nats/nats-mtls.conf                # NATS client + cluster mTLS config
├── .gitignore                         # blocks out/ and key material
└── README.md
```

## Trust model

UBAG terminates client **mTLS at the edge (Caddy)**. The gateway process listens
on plain HTTP on the private network (`UBAG_GATEWAY_ADDR=:8080`) and is never
internet-facing. Caddy verifies the client certificate against the UBAG CA and
forwards the verified subject to the gateway via `X-Client-Cert-Subject` for
application-level attribution. Backing-service links (gateway↔NATS,
gateway↔Postgres) use their own server-side TLS/mTLS (see `nats/nats-mtls.conf`
and your Postgres `sslmode=verify-full`).

## Generate dev certificates

```bash
# Linux / macOS
deploy/mtls/gen-certs.sh --cn ubag.example.com --client ubag-client

# Windows (openssl on PATH)
deploy\mtls\gen-certs.ps1 -Cn ubag.example.com -Client ubag-client
```

Output (in `deploy/mtls/out/`, gitignored):

| File | Purpose |
| --- | --- |
| `ca.crt` / `ca.key` | dev root CA (trust anchor) |
| `server.crt` / `server.key` | edge (Caddy) server cert |
| `client.crt` / `client.key` | API client cert |
| `client.p12` | client bundle (empty password) |

> **DEV ONLY.** For production use a real PKI (cert-manager, Vault, or a managed
> CA). Rotate and short-circuit lifetimes; never reuse the dev CA.

## Wire Caddy for mTLS (small profile)

```powershell
# in deploy\small\env.local
UBAG_EDGE_BIND_HOST=0.0.0.0
UBAG_CADDY_HTTP_PORT=80
UBAG_CADDY_HTTPS_PORT=443
UBAG_CADDYFILE=./deploy/mtls/caddy/Caddyfile.mtls.example
UBAG_PUBLIC_DOMAIN=ubag.example.com
```

Mount the generated certs into the Caddy container at `/etc/ubag/mtls` (read
only). Then test:

```bash
curl --cacert deploy/mtls/out/ca.crt \
     --cert  deploy/mtls/out/client.crt \
     --key   deploy/mtls/out/client.key \
     https://ubag.example.com/v1/health
```

A request without a valid client cert is rejected at the TLS handshake.

## Kubernetes note

On Kubernetes, prefer cert-manager-issued certs and enforce client auth at the
ingress controller (e.g. nginx `auth-tls-*` annotations) or a service mesh
(Istio/Linkerd) mTLS for in-cluster traffic. The same trust model applies: edge
verifies clients, mesh secures pod-to-pod.

## Validates offline

- `bash -n gen-certs.sh` (syntax); running it needs `openssl`.
- `caddy validate --config caddy/Caddyfile.mtls.example --adapter caddyfile`
  (needs the Caddy binary).

## Requires external infra

- Real DNS + a production CA for non-dev use.
- A reachable edge with the certs mounted to actually serve mTLS.
