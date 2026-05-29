# UBAG Kotlin SDK

Idiomatic Kotlin (JVM) client for the UBAG v0 REST gateway. Mirrors the
canonical [`sdk-go`](../sdk-go) contract: stable `UBAG-` error codes, automatic
`Idempotency-Key` generation for mutating calls, SDK metadata headers, and a
pluggable `Transport` for testing without a live gateway.

- API version header: `Ubag-Api-Version: 2026-05-22`
- SDK headers: `Ubag-Sdk-Name: ubag-kotlin`, `Ubag-Sdk-Version: 0.0.0`
- Auth: `Authorization: Bearer <app-secret>`

## Requirements

- JDK 17+
- Gradle (uses OkHttp + org.json at runtime)

## Usage

```kotlin
import com.ubag.sdk.UbagClient
import com.ubag.sdk.ListJobsParams

val client = UbagClient(
    baseUrl = "https://gateway.example.com",
    appSecret = System.getenv("UBAG_APP_SECRET"),
)

val health = client.health()

val job = client.createJob(
    mapOf("job" to mapOf("target" to "mock_target", "command_type" to "echo")),
)

val page = client.listJobs(ListJobsParams(status = "completed", limit = 20))

val artifact = client.getJobArtifact(job.getString("id"), "report.txt")
println("${artifact.contentType} ${artifact.checksum}")
```

### Error handling

```kotlin
import com.ubag.sdk.UbagApiException
import com.ubag.sdk.UbagTransportException

try {
    client.cancelJob("job_123", mapOf("reason" to "caller_cancelled"))
} catch (e: UbagApiException) {
    // e.status, e.code(), e.category(), e.retryable(), e.traceId()
} catch (e: UbagTransportException) {
    // network/transport failure before a response was received
}
```

### Custom transport

Implement the `Transport` functional interface to integrate a custom OkHttp
client, retry interceptor, or a test double:

```kotlin
val client = UbagClient("https://gateway.example.com", transport = Transport { request ->
    // return TransportResponse(status, headers, body)
})
```

## Testing

```bash
gradle test
```

Tests inject a `CapturingTransport` and assert request method, path, headers,
and body without contacting a gateway. Requires network for the first build
(Gradle resolves OkHttp, org.json, and JUnit 5); the test logic itself is
offline.
