# UBAG Swift SDK

Idiomatic Swift client for the UBAG v0 REST gateway, with **no external
dependencies** (Foundation only). Mirrors the canonical [`sdk-go`](../sdk-go)
contract: stable `UBAG-` error codes, automatic `Idempotency-Key` generation for
mutating calls, SDK metadata headers, and a pluggable `Transport` for testing
without a live gateway.

- API version header: `Ubag-Api-Version: 2026-05-22`
- SDK headers: `Ubag-Sdk-Name: ubag-swift`, `Ubag-Sdk-Version: 0.0.0`
- Auth: `Authorization: Bearer <app-secret>`

## Requirements

- Swift 5.7+

## Add to your project

```swift
// Package.swift
.package(path: "../sdk-swift"),
// or .package(url: "https://github.com/...", from: "0.0.0"),
```

## Usage

```swift
import Ubag

let client = UbagClient(
    baseURL: "https://gateway.example.com",
    appSecret: ProcessInfo.processInfo.environment["UBAG_APP_SECRET"]
)

let health = try await client.health()

let job = try await client.createJob([
    "job": ["target": "mock_target", "command_type": "echo"],
])

let page = try await client.listJobs(ListJobsParams(limit: 20, status: "completed"))

let artifact = try await client.getJobArtifact(job["id"] as! String, key: "report.txt")
print(artifact.contentType, artifact.checksum)
```

### Error handling

```swift
do {
    _ = try await client.cancelJob("job_123", request: ["reason": "caller_cancelled"])
} catch let error as UbagApiError {
    // error.status, error.code, error.category, error.retryable, error.traceId
} catch let error as UbagTransportError {
    // network/transport failure before a response was received
}
```

### Custom transport

Conform to `Transport` to integrate a custom `URLSession`, retry policy, or a
test double:

```swift
struct MyTransport: Transport {
    func send(_ request: HTTPRequest) async throws -> HTTPResponse { /* ... */ }
}

let client = UbagClient(baseURL: "https://gateway.example.com", transport: MyTransport())
```

## Testing

```bash
swift test
```

Tests inject a `CapturingTransport` and assert request method, path, headers,
and body without contacting a gateway. Builds and tests **fully offline** — no
package dependencies to resolve.
