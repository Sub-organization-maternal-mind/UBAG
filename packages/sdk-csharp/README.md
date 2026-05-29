# UBAG C# SDK

Idiomatic C# (.NET 8) client for the UBAG v0 REST gateway. Mirrors the canonical
[`sdk-go`](../sdk-go) contract: stable `UBAG-` error codes, automatic
`Idempotency-Key` generation for mutating calls, SDK metadata headers, and a
pluggable `ITransport` for testing without a live gateway.

- API version header: `Ubag-Api-Version: 2026-05-22`
- SDK headers: `Ubag-Sdk-Name: ubag-csharp`, `Ubag-Sdk-Version: 0.0.0`
- Auth: `Authorization: Bearer <app-secret>`

## Requirements

- .NET SDK 8.0+

## Usage

```csharp
using Ubag.Sdk;

var client = new UbagClient("https://gateway.example.com", new UbagClientOptions
{
    AppSecret = Environment.GetEnvironmentVariable("UBAG_APP_SECRET"),
});

var health = await client.HealthAsync();

var job = await client.CreateJobAsync(new Dictionary<string, object?>
{
    ["job"] = new Dictionary<string, object?> { ["target"] = "mock_target", ["command_type"] = "echo" },
});

var page = await client.ListJobsAsync(new ListJobsParams { Status = "completed", Limit = 20 });

var artifact = await client.GetJobArtifactAsync(job.GetProperty("id").GetString()!, "report.txt");
Console.WriteLine($"{artifact.ContentType} {artifact.Checksum}");
```

### Error handling

```csharp
try
{
    await client.CancelJobAsync("job_123", new Dictionary<string, object?> { ["reason"] = "caller_cancelled" });
}
catch (UbagApiException e)
{
    // e.Status, e.Code, e.Category, e.Retryable, e.TraceId
}
catch (UbagTransportException e)
{
    // network/transport failure before a response was received
}
```

### Custom transport

Implement `ITransport` to integrate a custom `HttpClient`, Polly policies, or a
test double:

```csharp
public sealed class MyTransport : ITransport
{
    public Task<TransportResponse> SendAsync(TransportRequest request, CancellationToken ct = default) => /* ... */;
}

var client = new UbagClient("https://gateway.example.com", new UbagClientOptions { Transport = new MyTransport() });
```

## Testing

```bash
dotnet test
```

Tests inject a `CapturingTransport` and assert request method, path, headers,
and body without contacting a gateway. Requires network for the first
`dotnet restore` (downloads xUnit + test SDK); the test logic itself is offline.
