---
title: SDK Conformance
description: SDK generation, feature parity, and conformance testing baseline.
---

# SDK Conformance

UBAG SDKs are schema-driven. Generated contract manifests pin OpenAPI, Protobuf, and JSON Schema freshness, while language-native clients provide the protocol surface and ergonomics for retries, streaming helpers, idempotency key generation, upload helpers, tracing hooks, and sidecar discovery.

## SDK Set

| Language or runtime | Package | Target ecosystems |
| --- | --- | --- |
| TypeScript / JavaScript | `@ubag/sdk` | Node, Bun, Deno, browser, Electron, Tauri, extensions |
| Python 3.10+ | `ubag` | PySide, Tk, Django, FastAPI, scripts |
| Go 1.21+ | `github.com/ubag/ubag-go` | Microservices, CLIs |
| Rust 1.78+ | `ubag` | Tauri, embedded, performance-sensitive clients |
| .NET 8 | `Ubag.Sdk` | WPF, WinForms, MAUI, Avalonia, ASP.NET |
| Java 17+ / Kotlin | `dev.ubag:ubag-sdk` | JavaFX, Spring, Android |
| Swift 5.9+ | `Ubag` | macOS, iOS, server-side Swift |
| Ruby 3.2+ | `ubag` | Rails, scripts |
| PHP 8.2+ | `ubag/ubag-sdk` | Laravel, WordPress plugins |
| Dart / Flutter | `ubag` | Flutter mobile and desktop |
| Elixir | `ubag` | Phoenix, BEAM systems |

## Required Feature Parity

Every SDK must support:

- Native sync and async idioms where the language supports both.
- Auto-retry with exponential backoff and jitter.
- Idempotency key auto-generation and caller override.
- Streaming through WebSocket or SSE using language-native callbacks, observables, channels, or async iterators.
- gRPC client support where ergonomic for the ecosystem.
- File upload through multipart and chunked paths.
- Webhook signature verification helpers.
- OpenTelemetry tracing hooks.
- Local sidecar discovery at `http://127.0.0.1:7878` before remote fallback.
- Offline queue where the platform can persist it safely.
- Pluggable HTTP client or transport where that is conventional in the ecosystem.

## Conformance Suite

The conformance suite is a shared JSON-defined test plan executed against each SDK and a mock gateway. The long-term suite targets 250 or more scenarios. The current v0 baseline contains 30 executable REST scenarios wired through TypeScript, Python, and Go SDK runners, generated operation-level contract freshness checks for all three packages, and 12 named non-executable coverage scenarios for retries, streaming, timeouts, Unicode, large payloads, malformed responses, webhook helpers, sidecar discovery, executor dispatch, file-spool/NATS worker ingestion, and webhook outbox retry.

Baseline categories:

| Category | Required coverage |
| --- | --- |
| Authentication | Missing credential, invalid credential, under-scoped credential, successful request |
| Versioning | Default SDK version, per-call override, unsupported version |
| Idempotency | Generated key, caller key, replay success, replay conflict, retry reuses key |
| Validation | Unknown fields, schema mismatch, command-specific validation failure |
| Jobs | Create, get, list, cancel, retry, completed result |
| Streaming | SSE events, WebSocket events, terminal state, stream failure |
| Errors | Every stable error namespace preserves code, category, retryable, details, trace id |
| Rate limiting | `429`, `Retry-After`, retry disabled, retry enabled |
| Webhooks | Signature generation, verification, timestamp rejection, duplicate delivery rejection |
| Unicode and large payloads | Unicode prompts, large responses, upload path, malformed responses |
| Timeouts | Client timeout, server timeout, retry exhaustion |
| Sidecar | Local discovery success, remote fallback, offline queue where supported |

## Pass Criteria

An SDK release is conformant only when:

- It passes the shared conformance plan for its supported transports.
- Unsupported optional transports are declared in metadata and skipped explicitly.
- Generated contract manifests match OpenAPI, proto, and JSON Schema hashes and REST path inventory.
- Errors preserve the full stable error envelope.
- Automatic retries never change the idempotency key for the same logical operation.
- Streaming APIs preserve event ordering per job.
- Webhook verification helpers use the documented base string and replay window.
- Conformance fixtures run in CI and block package publication on failure.

## Runner Contract

Each SDK owns a thin runner that translates the shared test plan into language-native calls.

The runner must expose:

- SDK package version.
- Default API version.
- Supported transports.
- Supported platform features, such as offline queue and sidecar discovery.
- A way to inject a mock gateway base URL.
- A way to disable automatic retries for deterministic cases.
- Raw access to response headers for rate limit and version assertions.

The runner should not contain scenario-specific behavior. Scenario expectations live in the shared conformance plan so parity remains enforceable across languages.
