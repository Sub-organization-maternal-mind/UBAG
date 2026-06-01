---
title: Plugins
description: WASM plugin host runtime, 6 hook kinds, and ubag plugins install.
---

## Status

**Implemented in Phase 6.** The WASM plugin host runs inside the gateway via a pure-Go wazero runtime (no CGO). See [ADR 0010](/adrs/0010-phase6-wasm-observability) for design decisions.

## Host model

UBAG uses WebAssembly core modules (WASI Preview 1) with a default-deny capability system. Plugins declare explicit permissions for network, filesystem, environment, time, and randomness. Unlisted host functions trap at instantiation.

**Runtime:** `github.com/tetratelabs/wazero` — pure Go, no CGO.

**Guest ABI v1:** host calls `alloc(size) → ptr`, writes input JSON, calls `transform(ptr, len) → u64` or `hook(evtPtr, evtLen, payloadPtr, payloadLen) → u64`, and reads the result from the packed `(resPtr << 32) | resLen` return.

## Plugin kinds (all 6 implemented)

| Capability | Hook event | Description |
|---|---|---|
| `transform.prompt` | transform "prompt" | Rewrite the job prompt before dispatch |
| `transform.response` | transform "response" | Normalise the job result |
| `hook.job.pre` | job.pre | Gate or enrich before job is created (reject → HTTP 400) |
| `hook.job.post` | job.post | Transform result after terminal state |
| `hook.webhook.transform` | webhook.transform | Shape webhook payloads per endpoint |
| `hook.validate` | validate | Domain-specific payload validation |

Additional capabilities `adapter.extension` and `command.custom` are defined for future routing metadata.

## Installing plugins

```bash
# List installed plugins
ubag plugins list

# Verify a plugin bundle signature
ubag plugins verify /path/to/plugin.manifest.json

# Install (verifies ed25519 signature + capability policy)
ubag plugins install /path/to/plugin.manifest.json

# Restrict allowed capabilities
ubag plugins install /path/to/plugin.manifest.json \
  --allow-capability hook.job.pre \
  --allow-capability hook.validate
```

Plugins install to `~/.ubag/plugins/<id>@<version>/`.

## Signature format

Each plugin bundle ships `manifest.json` + `<module>.wasm` + `manifest.json.sig`. The `.sig` file is 96 bytes base64-encoded: 64-byte ed25519 signature over `SHA256(manifest) || SHA256(wasm)`, followed by the 32-byte public key.

## Resource limits

All limits are declared in the plugin manifest:

```json
{
  "permissions": {
    "max_memory_bytes": 16777216,
    "max_execution_ms": 1000
  }
}
```

The gateway enforces memory via wazero's `WithMemoryLimitPages` and per-call deadlines via `context.WithTimeout`. A plugin that exceeds its budget is aborted; the executor is permanently closed after any timeout.
