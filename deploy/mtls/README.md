# UBAG mTLS

Mutual-TLS material and configuration for high-assurance UBAG deployments
(Tier 3+ per the blueprint: "mTLS for non-loopback bindings" and
"High-assurance server clients"). **No private keys are committed** — they are
generated at runtime and gitignored.

## Files

```
deploy/mtls/
├── gen-certs.sh                       # generate dev CA + server + client certs, or --node Helper Node certs (Linux/macOS)
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

## Helper Node certificates (dev) and the helper trust plane

The helper trust plane (`UBAG_HELPER_PLANE`, default off; ADR-0010) is a
**separate** mTLS gRPC listener on the gateway, not the Caddy edge and not the
plaintext `UBAG_GRPC_ADDR` server. A Helper Node is identified only by the
**URI SAN** of its client certificate, `spiffe://ubag/node/<node_id>`; the CN is
ignored. The gateway pins the certificate's SPKI SHA-256 in the node registry
(`current` and, during rotation, `next`) and re-checks the registry on every RPC
and every stream message, so revocation or a pin change takes effect on the next
message.

Certificate profile the helper plane accepts (the fleet manager's production CA
must issue exactly this; the production CA is **not** in this repo):

- leaf certificate, not a CA, signed by the CA in `UBAG_HELPER_CA_FILE`
- exactly one URI SAN, `spiffe://ubag/node/<node_id>` (`node_id` matches
  `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
- extended key usage includes `clientAuth` (and `serverAuth`: the primary also
  dials the helper)
- validity of at most 72 hours (a 10 minute backdating slack is tolerated)

Issue a dev node certificate (EC P-256, 3 days = 72h, the maximum):

```bash
# Linux / macOS
deploy/mtls/gen-certs.sh --node helper-1          # [--node-days 1..3] [--out DIR]
# Windows (openssl on PATH)
deploy\mtls\gen-certs.ps1 -Node helper-1          # [-NodeDays 1..3] [-Out DIR]
```

It reuses (or creates) `ca.crt` / `ca.key` in the output directory and writes
`node-<id>.crt`, `node-<id>.key` and `node-<id>.spki.sha256` (the pin, lowercase
hex). It prints the URI SAN, expiry and pin.

Gateway configuration (all four are required when `UBAG_HELPER_PLANE=true`, and
`UBAG_HELPER_NODES=true` must be on too):

| Variable | Meaning |
| --- | --- |
| `UBAG_HELPER_GRPC_ADDR` | listen address; bind it to the WireGuard interface |
| `UBAG_HELPER_CA_FILE` | PEM trust anchors for node certificates (here `out/ca.crt`) |
| `UBAG_HELPER_TLS_CERT_FILE` / `UBAG_HELPER_TLS_KEY_FILE` | the listener's own certificate; re-read when the files change |

The listener's own certificate is a normal server certificate (for example the
one `gen-certs.sh` already writes as `server.crt` / `server.key`).

### Rotation runbook (overlap window)

Rotate before the current certificate has less than a third of its life left
(at 48h of a 72h certificate). A stream whose certificate expires is ended at
its next message, so do not rely on expiry to retire a certificate.

1. Issue the next certificate under the same CA with a label:
   `gen-certs.sh --node helper-1 --label next` (`-Label next` on Windows).
2. Register its pin as the node's **next** SPKI (`PutRegistry` with `SPKINext`).
   From here both certificates are accepted: live streams on the old one keep
   running and the helper may connect with the new one.
3. Switch the helper to `node-helper-1.next.crt` / `.key` (new connections).
4. When no stream still uses the old certificate, promote the next pin
   (`PromoteSPKI`). The old certificate is rejected from then on, including on a
   stream that is still open (its next message fails with `Unauthenticated`).

Revoke a node with `RevokeNode` (sticky: a revoked node id is never re-admitted;
issue a new node id instead). A live stream of a revoked node ends at its next
message.

Registry writes are made through the node store (`internal/nodes`); an operator
route for them (`fleet:manage`) and the allocation poller that feeds pins from
manager grants are later slices.

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
