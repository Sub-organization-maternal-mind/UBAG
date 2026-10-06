# Helper Node workload (inert)

What UBAG hands the external fleet manager so it can run a Helper Node: one container image and one manifest. Nothing in this repo deploys it, and the UBAG primary does not read these files at run time.

| File | Purpose |
|---|---|
| `workload-manifest.json` | The workload: image, non-secret env, the one listen address, mounts, resource requests. |
| `workload-manifest.schema.json` | Its schema (`additionalProperties: false` throughout). |
| `Dockerfile` | `ubag-helper` + the warm Python worker + Playwright Chromium, non-root (`10001`), no EXPOSE. Build context is the repo root. |
| `../../.github/workflows/helper-image.yml` | Manual (`workflow_dispatch`) build of that image; pushes to GHCR only when `push` is ticked. |
| `../../tools/check-helper-deploy.mjs` | Static gate (`pnpm check:helper-deploy`, also part of `pnpm test:deployment`). |

## What the manager does (not UBAG)

- Pulls `ghcr.io/sub-organization-maternal-mind/ubag-helper:sha-<commit>` (the manifest's `{{image_tag}}`) and substitutes every `{{placeholder}}` after checking it against the parameter's `pattern`.
- Applies Docker limits. The manifest carries **requests only** (`cpu_millis`, `memory_bytes`, `shm_bytes`), sized for one browser workload. The grant the manager publishes (the node-allocation schema) is what the primary places against; UBAG's own ceiling table only narrows it.
- Applies the firewall: allow TCP to the WireGuard address on the `grpc` port from the primary only.
- Issues the node certificate (at most 72 h; URI SAN carries the node id) and mounts `ca.pem`, `node.crt`, `node.key` read-only at `/etc/ubag-helper/tls` from `tls_dir`. The image and the manifest contain no key, token or password.

## Network shape

The helper refuses a wildcard or hostname listen address (`UBAG_HELPER_LISTEN` must be one interface IP). A bridge-networked container cannot name its WireGuard address, so the manifest asks for `network.mode: host` and the listener binds `{{wireguard_address}}:7443` directly. There are no published ports. Voice (UDP range, NAT address) is not part of this manifest; it arrives with the helper voice slice.

## Operator login

Provider logins on a helper are human, out of band (noVNC over WireGuard or an SSH tunnel). That access is not provisioned here and the image ships no viewer. Safe-mode applies: no automated login, credential storage or CAPTCHA solving.

## Checks

```
node tools/check-helper-deploy.mjs
```

Validates the manifest against its schema; rejects secrets, wildcard addresses, limits, published ports and unknown or unused placeholders; requires every `UBAG_HELPER_*` name to be one `internal/helper/config.go` reads and every required setting to be supplied by the manifest or the image; checks the Dockerfile (numeric non-root user matching the manifest, no EXPOSE, no copied keys or env files, `HOME` and the profile root are mounts) and that the workflow can only be dispatched by hand with a read-only default token and a push that defaults to off.

The image build itself is not run by this check (no Docker in the gate).
