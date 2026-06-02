---
title: Plugin Author Guide
description: Complete guide to writing, testing, and publishing UBAG WASM plugins.
---

Plugins extend gateway and worker behavior via WebAssembly hooks. They run in a sandboxed
WASI environment with explicit permission grants — no arbitrary syscalls.

## When to write a plugin vs. an adapter

| Use a plugin when... | Use an adapter when... |
|---------------------|----------------------|
| You want to intercept all jobs (cross-cutting) | You're adding a new provider or command |
| You need custom auth/rate-limit logic | You need Playwright access |
| You want to transform inputs/outputs globally | You need full browser automation |

## Supported languages

- **Rust** (recommended — smallest WASM binary, best SDK support)
- **Go** (via TinyGo)
- **AssemblyScript**

## Plugin hooks

| Hook | Timing | Can deny? |
|------|--------|-----------|
| `before_job` | Before job is dispatched to worker | Yes |
| `after_job` | After job completes (success or failure) | No |
| `before_artifact_upload` | Before artifact is written to storage | Yes |
| `on_job_error` | When a job fails | No |

## Scaffold (Rust)

```bash
cargo generate --git https://github.com/ubag/plugin-template --name my-plugin
cd my-plugin
```

## Full example

```rust
use ubag_plugin_sdk::{plugin, JobSpec, deny, allow};

plugin!();

#[no_mangle]
pub extern "C" fn before_job(ptr: *const u8, len: usize) -> i32 {
    let spec: JobSpec = ubag_plugin_sdk::decode(ptr, len);

    // Block jobs targeting known-blocked domains
    if spec.target.contains("blocked.example.com") {
        return deny("Target is on the blocklist");
    }

    // Add a custom tag to the job
    ubag_plugin_sdk::set_tag("processed_by", "my-plugin");

    allow()
}
```

## Build and test

```bash
# Build WASM
cargo build --target wasm32-wasi --release

# Unit test (native target)
cargo test

# Integration test against a local gateway
ubag-cli plugin test \
  --wasm target/wasm32-wasi/release/my_plugin.wasm \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET
```

## Security model

Plugins are sandboxed:
- No filesystem access except plugin-provided scratch space
- No network access
- Memory limit: 16 MB per invocation
- CPU limit: 50 ms per hook call (configurable)

Permissions are declared in the plugin manifest and reviewed at install time.

## Publish

```bash
ubag-cli plugin publish \
  --wasm target/wasm32-wasi/release/my_plugin.wasm \
  --manifest plugin.json \
  --registry https://plugins.ubag.io
```

## Further reading

- [Author a Plugin (WASM)](/cookbook/27-plugin-authoring) — cookbook recipe
- [Install a Plugin](/cookbook/06-install-plugin) — deployment steps
- [Plugins](/plugins) — plugin system reference
