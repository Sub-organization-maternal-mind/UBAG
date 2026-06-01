---
title: "ADR 0010: Phase 6 — WASM Plugins & Observability"
description: Design decisions for the wazero-based Go plugin host and the OTel/Prometheus observability stack.
---

# ADR 0010: Phase 6 — WASM Plugins & Observability

**Status:** Accepted  
**Date:** 2026-06-02  
**Author:** UBAG Platform Team

---

## Context

Phase 6 operationalises two cross-cutting systems:
1. **WASM plugin host** — plugins declared in `packages/plugins` (TS) now also execute inside the Go gateway via a wazero-backed runtime.
2. **Observability** — the gateway and worker emit contract-conformant structured logs, OTLP traces, and the full Prometheus metric set defined in `packages/observability`.

This ADR records the key decisions made during implementation.

---

## Decision 1 — Runtime: wazero (pure Go, no CGO)

**Decision:** The Go plugin host uses `github.com/tetratelabs/wazero` targeting **core-module / WASI Preview 1** binaries.

**Rationale:**
- Pure-Go implementation: no CGO, no OS-level WASM runtime, CI toolchain is standard Go.
- The manifest schema already enumerates `entrypoint.type: "core-module"` and `engine.runtime: "wasi-preview1"`, so no schema change was needed.
- wazero does not yet implement the full WASI Preview 2 / component model; that path remains with the TS `@wasmer/wasi` host until wazero matures.

**Consequence:** Plugins targeting Preview 2 component APIs will not run through the Go gateway host in this phase.

---

## Decision 2 — Guest ABI v1 (core-module string passing)

**Decision:** The host/guest contract for core-module plugins is:

1. Host calls `alloc(inputLen i32) → ptr i32` to obtain a write pointer.
2. Host writes input JSON to `ptr` in guest linear memory.
3. Host calls `transform(ptr i32, len i32) → u64` or `hook(evtPtr, evtLen, payloadPtr, payloadLen i32) → u64`.
4. Guest returns a packed `(resultPtr << 32) | resultLen` u64.
5. Host reads the result bytes from guest memory.

Host-imported functions (`log`, `clock`, `random`, `fetch`, `read_file`, `get_env`) are linked **only** if listed in `manifest.permissions.host_functions`; unlisted imports trap at instantiation.

**Rationale:** This ABI is already implied by the WIT contract comments and matches what the TS host does conceptually. Choosing a simple linear-memory protocol keeps the binary format stable and avoids custom serialisation overhead.

---

## Decision 3 — Test fixtures: committed `.wasm` binaries

**Decision:** CI does not require a WASM toolchain. A hand-crafted `echo_transform.wat` (with `wat2wasm` build instructions) is committed alongside its pre-compiled `echo_transform.wasm`. The same pattern applies to `infinite_loop.wasm` and `import_denied.wasm` test fixtures.

**Rationale:** Running `wat2wasm` in CI would require pinning the WABT version. Committing small (<100 byte) binaries is a reasonable trade-off for a stable, reproducible test suite.

---

## Decision 4 — Additive capability enum extension

**Decision:** Four new capabilities were added to the shared schema and both the TS and Go validators without removing or renaming any existing capability:
- `hook.webhook.transform`
- `hook.validate`
- `adapter.extension`
- `command.custom`

**Rationale:** Backwards compatibility — existing plugin manifests continue to validate against the new schema. Plugins only declare the capabilities they implement.

---

## Decision 5 — OTel sampling policy

**Decision:** `ParentBased(ErrorOrRatio{ratio=0.10})` — root spans are sampled at 10% unless the caller explicitly marks the context with `obs.SampleError(ctx)` (or `obs.ForceSample(ctx)`), in which case the span is always sampled.

**Rationale:** 10% captures enough traffic for latency percentiles without overwhelming the OTLP collector. Errors are always captured for debugging. The "100% on error" is implemented as a context flag rather than a post-hoc decision because wazero span context must be set before execution.

---

## Decision 6 — Dashboard and infrastructure path: `deploy/`

**Decision:** Grafana dashboards live under `deploy/grafana/dashboards/`. The docker-compose-level profiling/log-shipping services (Pyroscope, GlitchTip, Vector) are wired into `docker-compose.small.yml` under the `observability` profile.

**Rationale:** The repo uses `deploy/` (not `infra/`) as the infrastructure root. Grafana provisioning already existed at `deploy/small/grafana/`; the Phase 6 dashboards extend that layout to `deploy/grafana/` so they are shared across all deployment sizes.

---

## Consequences

- Plugins load and execute inside the Go gateway with capability gating and per-call time/memory budgets.
- All 6 hook kinds have call sites wired into the gateway lifecycle with TDD coverage.
- The `ubag plugins` CLI can install and verify signed plugin bundles.
- Gateway and worker emit redacted JSON logs and OTLP traces sharing a W3C `traceparent` trace ID.
- All 22 contract Prometheus metrics are emitted; cardinality budget is enforced by the CI checker.
- 9 Grafana dashboards are provisioned automatically in the `observability` profile.
- The SLO failure-budget math is unit-tested and exposed as `ubag_synthetic_slo_*` metrics.
