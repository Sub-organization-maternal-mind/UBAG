# UBAG Sidecar (Rust)

`ubag-sidecar` is the **Local Sidecar Connector** from the UBAG blueprint
(§3.1): a loopback-only localhost bridge for legacy desktop apps and scripts
that cannot use the full UBAG SDK directly. This is the Rust port of the
TypeScript reference at [`packages/sidecar`](../sidecar), built on `tokio` +
`axum` for the smallest possible service binary.

It exposes two surfaces:

- `GET /health` — sidecar health and the configured gateway target.
- `/v1/*` — a transparent reverse proxy to the configured UBAG gateway.

## Behaviour (1:1 with the TypeScript sidecar)

- **Loopback-only by default.** The process refuses to bind a non-loopback
  interface unless `--allow-non-loopback` is passed after a firewall review.
- **Auto idempotency.** For mutating proxy routes the sidecar generates a
  ULID-style `Idempotency-Key` when the client did not supply one, and injects
  the same value as `idempotency_key` into JSON object request bodies. Matched
  routes (identical to the TS rules):
  - `POST /v1/jobs`
  - `POST /v1/webhooks/replay`
  - `POST /v1/jobs/{id}/cancel`
  - `POST /v1/jobs/{id}/retry`
  - `PUT /v1/jobs/{id}/artifacts/{name}`
  - `DELETE /v1/jobs/{id}/artifacts/{name}`
- **Header forwarding.** Every client header except `Host` and `Content-Length`
  is forwarded to the gateway (including `Authorization` and
  `UBAG-Api-Version`). The sidecar adds `X-Ubag-Sidecar: loopback`.
- **Hop-by-hop stripping.** `connection`, `content-encoding`, `content-length`,
  `keep-alive`, `proxy-authenticate`, `proxy-authorization`, `te`, `trailer`,
  `transfer-encoding`, and `upgrade` are stripped from the upstream response.
- **Absolute-form guard.** Absolute-form proxy requests are only honoured when
  they target a loopback host; anything else is rejected with `502`.

## Toolchain

- Rust **1.78+** with `cargo` (install via [rustup](https://rustup.rs/)).
- No external services are required to build or test — the test suite spins up
  an in-process mock upstream on an ephemeral loopback port.

## Build

```bash
cargo build --release
# binary at target/release/ubag-sidecar
```

## Run

```bash
# Defaults: --host 127.0.0.1 --port 7878 --gateway http://127.0.0.1:8080
ubag-sidecar

# Explicit configuration:
ubag-sidecar --host 127.0.0.1 --port 7878 --gateway http://127.0.0.1:8080
```

### Configuration

CLI flags take precedence over environment variables:

| Flag | Env | Default | Notes |
| --- | --- | --- | --- |
| `--host` | `UBAG_SIDECAR_HOST` | `127.0.0.1` | Must be loopback unless opted in. |
| `--port` | `UBAG_SIDECAR_PORT` | `7878` | |
| `--gateway` | `UBAG_GATEWAY_URL` | `http://127.0.0.1:8080` | Upstream gateway base URL. |
| `--allow-non-loopback` | — | `false` | Firewall-reviewed opt-in only. |
| `--app-secret` | `UBAG_APP_SECRET` | — | Optional bearer token passthrough. |

## Tests

```bash
cargo test
```

Covers idempotency auto-generation for every mutating route (including artifact
`PUT`/`DELETE`), the loopback-binding guard, header forwarding/stripping, the
app-secret passthrough, and `/health`. All tests run offline against an
in-process mock upstream.

## OS service install

Templated unit files live under `deploy/`:

- **systemd** — `deploy/systemd/ubag-sidecar.service`
  (substitute `@USER@`, `@PREFIX@`, `@ENVFILE@`; mirrors the hardened gateway
  unit in [`deploy/installers/systemd`](../../deploy/installers/systemd)).
- **macOS launchd** — `deploy/launchd/dev.ubag.sidecar.plist`
  (substitute `@PREFIX@`; load with `launchctl load`).
- **Windows service** — register the release binary with the Service Control
  Manager, e.g.

  ```powershell
  sc.exe create UbagSidecar binPath= "C:\Program Files\UBAG\ubag-sidecar.exe" start= auto
  sc.exe start UbagSidecar
  ```

  Pass configuration via machine/service environment variables
  (`UBAG_GATEWAY_URL`, `UBAG_SIDECAR_HOST`, `UBAG_SIDECAR_PORT`).

See [`deploy/installers/README.md`](../../deploy/installers/README.md) for the
broader installer conventions.

## Security model

- **Loopback-only.** Binding is restricted to loopback interfaces unless
  `--allow-non-loopback` is explicitly set after a firewall review.
- **No secret persistence.** The optional application secret
  (`UBAG_APP_SECRET` / `--app-secret`) is held in memory only and is never
  written to disk. It is injected as `Authorization: Bearer <secret>` *only*
  when the incoming client request omits an `Authorization` header; a
  client-supplied token is always preserved.
- **Keychain integration (TODO).** OS keychain backends (Windows Credential
  Manager, macOS Keychain, libsecret) can replace the env-based secret source
  by implementing the `SecretProvider` trait seam and passing it to
  `build_app_with_secrets`. The default `EnvSecretProvider` reads the in-memory
  configuration value. No other call sites change.
