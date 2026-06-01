---
title: "ADR 0009: Phase 5 — Go CLI, Edge Single-Binary, and Rust Sidecar Enhancements"
description: Documents the CLI-in-gateway-module architecture, Python worker subprocess, file-spool edge executor, and optional sidecar feature flags.
---

## Status

Accepted (2026-06-02).

## Context

Phase 5 delivers three interrelated pieces: the Go CLI (blueprint §29 Bubble Tea
terminal UI), the `ubag` single-binary edge entry point (blueprint §29.1
"single-process embedding"), and three optional hardening features for the Rust
sidecar. Several structural constraints arose from Go module boundaries, the
Python worker runtime, and the edge deployment's SQLite requirement that
necessitate deliberate deviations from the blueprint's directory layout.

## Decision

### Decision 1 — CLI lives inside the gateway Go module (`apps/gateway/internal/cli/`), not in a separate `apps/cli/` module

The blueprint references an `apps/cli/` path for the Go CLI. In practice, Go
forbids importing another module's `internal/` packages. A standalone
`apps/cli` module could only shell out to a separate gateway process, which
defeats the §29.1 "single-process embedding" goal. Only code rooted at
`apps/gateway/` can call `internal/httpapi`, `internal/profile`,
`internal/serve`, etc. in-process. Accordingly:

- `apps/gateway/internal/cli/` contains the command dispatch logic and
  Bubble Tea TUI components.
- `apps/gateway/cmd/ubag/` is the binary entry point that wires the CLI into
  the single `ubag` executable.

This is a deliberate, documented deviation from the blueprint's `apps/cli/`
path. The blueprint intent (a Go CLI sharing the gateway codebase) is
preserved; only the directory differs.

### Decision 2 — Python worker runs as a managed subprocess, not embedded

The browser automation worker is written in Python and cannot be linked into a
Go binary. `ubag start` launches it by setting the environment variable
`UBAG_WORKER_CONSUMER_ENABLED=true`, which causes the gateway to spawn the
Python process as a managed subprocess through the existing
`executor.WorkerConsumer` path. This satisfies the §29.1 requirement that
"lightweight mode must always work" without requiring a Python-in-Go embedding
solution.

### Decision 3 — Edge executor uses file-spool dispatcher, not River

`ubag start` sets `UBAG_EXECUTOR_MODE=file` and
`UBAG_EXECUTOR_SPOOL_DIR=~/.ubag/spool`. River requires a live Postgres
connection and is not appropriate for the single-process SQLite edge
deployment. The existing `executor.FileSpoolDispatcher` is SQLite-safe and
requires no external dependencies, making it the correct choice for the
`edge` deployment profile. River remains the executor for `standard` and
`enterprise` profiles where Postgres is available.

### Decision 4 — Rust sidecar uses optional feature flags for keychain, offline queue, and UDS

Three production-hardening capabilities are gated behind Cargo feature flags
so the default release binary remains small (<5 MB without any optional
features):

- **`keychain`** — OS-keychain secret backend via the `keyring` crate (Windows
  Credential Manager, macOS Keychain, libsecret on Linux). Falls back to
  `EnvSecretProvider` transparently when no keychain entry exists. Enables
  zero-plaintext-on-disk credential storage for production deployments.

- **`offline`** — disk-backed offline queue via the `sled` embedded database.
  When a gateway transport error occurs, the outbound request is serialised and
  queued for retry on reconnect. Suitable for intermittently-connected edge
  nodes.

- **`uds`** — Unix domain socket listener alongside the existing TCP listener
  (Unix only). A filesystem socket is inherently local, so the loopback-only
  guard is bypassed for UDS connections. Enables low-latency IPC from co-located
  processes without the TCP stack overhead.

Production deployments opt in with `cargo build --release --features=keychain,offline,uds`
or `make sidecar-build` (which uses `cargo build --release`; features are
added per environment).

## Consequences

- The TypeScript `@ubag/cli` package (`packages/cli/`) is superseded by the
  Go `ubag` binary and is marked deprecated. It remains available for
  backward-compatibility but will not receive new features.
- `make ubag-build` and `make sidecar-build` are added to the Makefile as
  canonical build targets.
- The `gateway` CI job (`go test ./...`) covers `internal/cli`, `internal/serve`,
  and `internal/cli/tui` automatically — no additional CI job is required for
  the CLI.
- The `sidecar-rust` CI job runs `cargo test --all-features` to exercise all
  three optional feature paths on every push.
- Future phases that add standard/enterprise CLI subcommands must place them
  under `apps/gateway/internal/cli/` to preserve in-process access to gateway
  internals.
