---
title: Author a Plugin (WASM)
description: Write a UBAG WASM plugin to intercept job lifecycle hooks and extend gateway behavior.
---

UBAG plugins are WebAssembly modules compiled from Rust, Go, or AssemblyScript that
hook into the job lifecycle at the gateway level.

## Scaffold (Rust)

```bash
cargo generate --git https://github.com/ubag/plugin-template --name my-plugin
cd my-plugin
```

## Plugin interface (Rust)

```rust
use ubag_plugin_sdk::{plugin, JobSpec, JobEvent, HookResult};

plugin!();

#[no_mangle]
pub extern "C" fn before_job(spec_ptr: *const u8, spec_len: usize) -> i32 {
    let spec: JobSpec = ubag_plugin_sdk::decode(spec_ptr, spec_len);

    // Validate or transform the job spec
    if spec.target.contains("blocked-domain.com") {
        return ubag_plugin_sdk::deny("Target is blocked");
    }

    ubag_plugin_sdk::allow()
}

#[no_mangle]
pub extern "C" fn after_job(event_ptr: *const u8, event_len: usize) -> i32 {
    let event: JobEvent = ubag_plugin_sdk::decode(event_ptr, event_len);

    // Emit custom metrics or post-process artifacts
    eprintln!("Job {} completed with status {:?}", event.job_id, event.status);

    ubag_plugin_sdk::allow()
}
```

## Build

```bash
cargo build --target wasm32-wasi --release
# Output: target/wasm32-wasi/release/my_plugin.wasm
```

## Plugin manifest

```json
{
  "id": "my-plugin",
  "version": "1.0.0",
  "hooks": ["before_job", "after_job"],
  "permissions": ["read_job_spec", "read_job_event"]
}
```

## Install

```bash
ubag-cli plugin install \
  --gateway http://localhost:8081 \
  --token $UBAG_APP_SECRET \
  --file target/wasm32-wasi/release/my_plugin.wasm \
  --manifest plugin.json
```

## Available hooks

| Hook | Description |
|------|-------------|
| `before_job` | Validate/transform job spec before execution |
| `after_job` | Post-process job result or artifacts |
| `before_artifact_upload` | Intercept artifact before storage |
| `on_job_error` | Custom error handling / alerting |

See [Plugins](/plugins) for the full plugin system reference.
See [Install a Plugin](/cookbook/06-install-plugin) for deployment steps.
