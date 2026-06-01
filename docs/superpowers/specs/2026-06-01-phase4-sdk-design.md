# Phase 4 — Client SDKs Design Spec
**Date:** 2026-06-01  
**Scope:** TypeScript, Go, Rust SDKs to full §8 parity + conformance suite expansion  
**Approach:** Conformance-first (250+ scenarios), then SDK implementation; hand-written code with auto-generated contract manifest

---

## 1. Goals

- Expand the conformance suite from ~15 to 250+ scenarios covering all 20 feature categories
- Auto-generate `generated_contract_manifest.*` in TypeScript, Go, and Rust from the OpenAPI spec so stale SDKs fail CI immediately
- Bring TypeScript, Go, and Rust SDKs to full §8 parity: retry, streaming SSE, gRPC, webhook verify, sidecar discovery, offline queue, OTel hooks, pluggable transport
- All SDK features must pass conformance tests against a mock server — no live gateway required

---

## 2. Non-goals

- Java, Kotlin, C#, Ruby, PHP, Python, Swift, Elixir, Dart SDKs (out of scope for this phase)
- Real browser or network tests (conformance uses mock servers only)
- Dashboard, CLI, or infrastructure changes

---

## 3. Phase 4a — Infrastructure

### 3.1 Conformance suite expansion

**File:** `packages/conformance/fixtures/v0/scenarios.json`  
**Current:** ~15 scenarios across 15 categories  
**Target:** 250+ scenarios across 20 categories

Category targets:

| Category | Count | Key scenarios |
|---|---|---|
| Job lifecycle | 20 | create, get, cancel, retry, terminal states |
| Auth | 15 | PAT, API key, app JWT, bearer, missing, expired |
| Retry & backoff | 20 | transient codes, permanent codes, max attempts, Retry-After, DLQ |
| Streaming SSE | 20 | event ordering, terminal event, reconnect, empty stream |
| Webhooks | 15 | HMAC verify, replay rejection, timestamp tolerance, missing sig |
| Idempotency | 15 | key generation, dedup, conflict (different payload), replay |
| Scheduling | 10 | not_before future, not_before past, not_before missing |
| Concurrency & backpressure | 10 | 429 on ceiling, 429 on queue depth, Retry-After header |
| Semantic cache | 10 | exact hit, miss, tag invalidation, TTL expiry |
| Template render | 10 | success, missing template 404, syntax error 400 |
| Priority lanes | 10 | crit/high/norm/low/bulk routing, anti-starvation |
| Pagination & filtering | 10 | cursor, limit, filter by status, sparse fieldsets |
| Error catalog | 20 | all 16 namespaces, retryable flag, retry_after_ms |
| gRPC | 15 | CreateJob, StreamJobEvents, CancelJob, status mapping |
| Sidecar discovery | 10 | loopback found, loopback absent, health probe failure |
| Offline queue | 10 | enqueue while offline, flush on reconnect, disk persistence |
| OTel hooks | 10 | span created, traceparent propagated, attributes set |
| Unicode & large payloads | 10 | UTF-8 roundtrip, >1MB rejection, trace ID preservation |
| Malformed responses | 10 | non-JSON body, 5xx with no body, truncated SSE |
| Compliance/privacy | 10 | export receipt, erase receipt, PHI cache skip |

Each scenario has: `id`, `category`, `title`, `description`, `request` (method+path+body), `expected` (status+body shape+headers), `assertions` array.

The existing Node.js validator (`packages/conformance/scripts/validate-fixtures.mjs`) runs these in the `node-suite` CI job — no runner changes needed.

### 3.2 Contract manifest generator

**File:** `tools/make-sdks/generate-manifest.mjs`  
**Inputs:**
- `packages/openapi/openapi.yaml` — endpoint registry
- `packages/shared-schemas/errors.json` — error code catalog
- `packages/shared-schemas/schemas/job-request.schema.json` — schema fingerprint
- `packages/shared-schemas/schemas/job-response.schema.json` — schema fingerprint

**Outputs (one per SDK):**
- `packages/sdk-typescript/src/generated/contract-manifest.ts`
- `packages/sdk-go/generated_contract_manifest.go`
- `packages/sdk-rust/src/generated/contract_manifest.rs`

**Manifest contents (all SDKs):**
```
API_VERSION: "2026-05-22"
ENDPOINTS: map of operationId → { method, path }
ERROR_CODES: map of code → { category, retryable, retry_after_ms? }
SCHEMA_FINGERPRINTS: map of schemaName → sha256hex
```

**CI gate:** `tools/check-contracts.mjs` (already in CI `lint` job) verifies that the manifest in each SDK matches the output of `generate-manifest.mjs`. A stale SDK fails the lint job.

**Run command:** `node tools/make-sdks/generate-manifest.mjs` — generates all three manifests in one pass.

---

## 4. Phase 4b — SDK Implementation

### 4.1 Feature set (all three SDKs)

Every feature below is implemented in TypeScript first, then ported to Go, then Rust. Each feature is covered by conformance scenarios before it is implemented.

| Feature | TypeScript | Go | Rust |
|---|---|---|---|
| Retry + backoff | `RetryPolicy` config | `RetryPolicy` struct | `RetryPolicy` builder |
| Streaming SSE | `AsyncIterable<JobEvent>` | `<-chan JobEvent` | `impl Stream<Item=...>` |
| gRPC client | `UbagGrpcClient` class | `GrpcClient` struct | `tonic`-backed client |
| Webhook verify | `verifyWebhookSignature()` | `VerifyWebhookSignature()` | `fn verify_webhook_signature()` |
| Sidecar discovery | auto-probe on construct | `DiscoverSidecar(ctx)` | probed via `tokio::time::timeout` |
| Offline queue | `OfflineQueue` + `StorageAdapter` | `OfflineStore` interface | `sled`-backed `OfflineQueue` |
| OTel hooks | `{ tracer: Tracer }` option | `otelhttp` transport wrap | `tracing-opentelemetry` |
| Pluggable transport | `transport` option | `Transport` interface | `Transport` trait |

### 4.2 TypeScript SDK

**Package:** `packages/sdk-typescript/`

**Retry (`src/retry.ts`):**
- `RetryPolicy { maxAttempts: number, baseDelayMs: number, maxDelayMs: number }`
- Defaults: 3 attempts, 1000ms base, 60000ms max, ±30% jitter
- Only retries on UBAG error codes where `retryable: true` (from contract manifest)
- Honors `Retry-After` header (overrides computed delay)

**Streaming (`src/streaming.ts`):**
- `client.streamJob(jobId: string): AsyncIterable<JobEvent>`
- Uses the browser-native `EventSource` or Node.js `eventsource` polyfill
- Auto-closes iterator on terminal event types (`completed`, `failed`, `cancelled`, `dead_letter`)
- Emits `ErrorEvent` for SSE error frames

**gRPC (`src/grpc.ts`):**
- `UbagGrpcClient` — wraps the proto-generated TypeScript stubs from `packages/proto/gen/`
- Constructor accepts `{ host: string, credentials?: ChannelCredentials }`
- Same retry and OTel hooks as the REST client

**Webhook verify (`src/webhooks.ts`):**
- `verifyWebhookSignature(payload: Buffer, sig: string, secret: string): boolean`
- HMAC-SHA256 using Node.js `crypto.timingSafeEqual`
- Validates `ubag-timestamp` header within 5-minute tolerance

**Sidecar discovery (`src/sidecar.ts`):**
- On `new UbagClient(options)`, probes `http://127.0.0.1:7878/v1/health` with 200ms AbortSignal timeout
- If probe succeeds, uses `http://127.0.0.1:7878` as base URL regardless of `options.baseUrl`
- Can be disabled via `options.sidecarDiscovery = false`

**Offline queue (`src/offline.ts`):**
- `StorageAdapter` interface: `{ read(): JobQueueEntry[], write(entries): void }`
- `FilesystemAdapter` ships with SDK: writes to `~/.ubag/offline-queue.json`
- `OfflineQueue` wraps the client: queues `createJob` when the gateway returns network error; flushes on next successful request
- Queue entries: `{ id, request, enqueuedAt, attempts }`

**OTel (`src/telemetry.ts`):**
- `telemetry?: { tracer: Tracer }` on client constructor
- Wraps every HTTP request in a span: name `ubag.{operationId}`, attributes `ubag.job_id`, `ubag.tenant_id`, `http.status_code`
- Injects W3C `traceparent` header on every request

### 4.3 Go SDK

**Package:** `packages/sdk-go/`

Idiomatic Go port of the TypeScript feature set:

- `RetryPolicy struct { MaxAttempts int; BaseDelay, MaxDelay time.Duration }` — passed to `NewClient()`
- `client.StreamJob(ctx context.Context, jobID string) (<-chan JobEvent, <-chan error)` — goroutine-based SSE consumer
- `GrpcClient struct` — wraps proto-generated Go stubs; `NewGrpcClient(host string, opts ...grpc.DialOption)`
- `VerifyWebhookSignature(payload []byte, sig, secret string) bool` — `crypto/hmac` + `crypto/sha256`
- Sidecar discovery: `discoverSidecar(ctx)` called in `NewClient()` with `http.Client{Timeout: 200ms}`
- Offline queue: `OfflineStore` interface; `FileOfflineStore` writes JSON to `$HOME/.ubag/offline-queue.json`
- OTel: `otelhttp.NewTransport(transport)` wraps the HTTP transport; span attributes injected via `trace.SpanFromContext`

### 4.4 Rust SDK

**Package:** `packages/sdk-rust/`

Async-first with `tokio`. Dependencies added: `tonic`, `sled`, `opentelemetry`, `tracing-opentelemetry`, `futures-core`.

- `RetryPolicy { max_attempts: u32, base_delay: Duration, max_delay: Duration }` — builder pattern
- `client.stream_job(job_id: &str) -> impl Stream<Item = Result<JobEvent, Error>>` — `futures_core::Stream` via `eventsource-client` crate
- `UbagGrpcClient` — wraps `tonic`-generated stubs from `packages/proto/gen/`; same retry policy
- `fn verify_webhook_signature(payload: &[u8], sig: &str, secret: &str) -> bool` — `hmac` + `sha2` crates
- Sidecar discovery: `tokio::time::timeout(Duration::from_millis(200), probe_health()).await`
- Offline queue: `sled::Db`-backed `OfflineQueue`; entries serialized as `serde_json`
- OTel: `opentelemetry::global::tracer("ubag-sdk")` + `tracing_opentelemetry::OpenTelemetrySpanExt`

### 4.5 Implementation order within each SDK

1. Contract manifest (generated file, CI gate)
2. Retry + backoff
3. Streaming SSE
4. Webhook verify
5. Idempotency key auto-generation
6. Sidecar discovery
7. Offline queue
8. gRPC client
9. OTel hooks
10. Pluggable transport

---

## 5. Testing strategy

**Conformance test runner per SDK:**
- TypeScript: `packages/sdk-typescript/tests/conformance.test.ts` — uses `msw` (Mock Service Worker) for mock HTTP, `@grpc/grpc-js` test server for gRPC
- Go: `packages/sdk-go/client_conformance_test.go` — uses `httptest.NewServer` for HTTP, `google.golang.org/grpc/test` for gRPC
- Rust: `packages/sdk-rust/tests/conformance.rs` — uses `wiremock` crate for HTTP, `tonic::transport::Server` in-process for gRPC

All conformance tests load the same `packages/conformance/fixtures/v0/scenarios.json` — no scenario is SDK-specific.

**CI:** The existing `node-suite` job runs TypeScript conformance. Go conformance runs in the existing `gateway` job (Go tests). Rust conformance runs in a new `sdk-rust` CI job added to `ci.yml`.

---

## 6. File changes summary

**New files:**
- `tools/make-sdks/generate-manifest.mjs`
- `packages/conformance/fixtures/v0/scenarios.json` (expanded in-place)
- `packages/sdk-typescript/src/retry.ts`
- `packages/sdk-typescript/src/streaming.ts`
- `packages/sdk-typescript/src/grpc.ts`
- `packages/sdk-typescript/src/webhooks.ts`
- `packages/sdk-typescript/src/sidecar.ts`
- `packages/sdk-typescript/src/offline.ts`
- `packages/sdk-typescript/src/telemetry.ts`
- `packages/sdk-typescript/src/generated/contract-manifest.ts` (generated)
- `packages/sdk-go/retry.go`, `streaming.go`, `grpc.go`, `webhooks.go`, `sidecar.go`, `offline.go`, `telemetry.go`
- `packages/sdk-go/generated_contract_manifest.go` (generated)
- `packages/sdk-rust/src/retry.rs`, `streaming.rs`, `grpc.rs`, `webhooks.rs`, `sidecar.rs`, `offline.rs`, `telemetry.rs`
- `packages/sdk-rust/src/generated/contract_manifest.rs` (generated)

**Modified files:**
- `packages/sdk-typescript/src/client.ts` — wire all new modules
- `packages/sdk-typescript/src/index.ts` — export new public API
- `packages/sdk-go/client.go` — wire all new modules
- `packages/sdk-go/go.mod` — add otelhttp, grpc deps
- `packages/sdk-rust/src/lib.rs` — wire all new modules
- `packages/sdk-rust/Cargo.toml` — add tonic, sled, opentelemetry deps
- `.github/workflows/ci.yml` — add `sdk-rust` job

---

## 7. Success criteria

- `node tools/make-sdks/generate-manifest.mjs` generates all 3 manifests without error
- `packages/conformance/fixtures/v0/scenarios.json` contains ≥250 valid scenarios
- All TypeScript SDK conformance tests pass (250+ scenarios)
- All Go SDK conformance tests pass (250+ scenarios)
- All Rust SDK conformance tests pass (250+ scenarios)
- `ci.yml` lint job fails when a manifest is stale
- No live gateway or browser required for any test
