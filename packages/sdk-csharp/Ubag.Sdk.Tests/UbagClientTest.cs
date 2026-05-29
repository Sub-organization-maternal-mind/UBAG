using System.Text;
using System.Text.Json;
using Ubag.Sdk;
using Xunit;

namespace Ubag.Sdk.Tests;

/// <summary>Capturing transport that records the last request and returns a canned response.</summary>
internal sealed class CapturingTransport : ITransport
{
    private readonly int _status;
    private readonly byte[] _body;
    private readonly IReadOnlyDictionary<string, string> _headers;

    public CapturingTransport(int status, string body, IReadOnlyDictionary<string, string>? headers = null)
    {
        _status = status;
        _body = Encoding.UTF8.GetBytes(body);
        _headers = headers ?? new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
    }

    public TransportRequest? LastRequest { get; private set; }

    public Task<TransportResponse> SendAsync(TransportRequest request, CancellationToken cancellationToken = default)
    {
        LastRequest = request;
        return Task.FromResult(new TransportResponse(_status, _headers, _body));
    }
}

public sealed class UbagClientTest
{
    private static UbagClient NewClient(CapturingTransport transport) =>
        new("http://127.0.0.1:7878/", new UbagClientOptions
        {
            AppSecret = "app_secret_fixture",
            Transport = transport,
        });

    [Fact]
    public async Task HealthSendsVersionAndAuthHeaders()
    {
        var transport = new CapturingTransport(200, "{\"status\":\"ok\"}");
        var result = await NewClient(transport).HealthAsync();

        Assert.Equal("ok", result.GetProperty("status").GetString());
        var request = transport.LastRequest!;
        Assert.Equal("GET", request.Method);
        Assert.Equal("http://127.0.0.1:7878/v1/health", request.Url);
        Assert.Equal(UbagClient.ApiVersion, request.Headers["Ubag-Api-Version"]);
        Assert.Equal(UbagClient.SdkName, request.Headers["Ubag-Sdk-Name"]);
        Assert.Equal("Bearer app_secret_fixture", request.Headers["Authorization"]);
        Assert.Null(request.Body);
    }

    [Fact]
    public async Task VersionOmitsIdempotencyKey()
    {
        var transport = new CapturingTransport(200, "{\"version\":\"0.0.0\"}");
        var options = new RequestOptions { IdempotencyKey = "ignored" };
        await NewClient(transport).VersionAsync(options);

        var request = transport.LastRequest!;
        Assert.Equal("http://127.0.0.1:7878/v1/version", request.Url);
        Assert.False(request.Headers.ContainsKey("Idempotency-Key"));
    }

    [Fact]
    public async Task CreateJobInjectsVersionIdempotencyAndSdkMetadata()
    {
        var transport = new CapturingTransport(202, "{\"status\":\"queued\"}");
        var body = new Dictionary<string, object?>
        {
            ["client"] = new Dictionary<string, object?> { ["app_id"] = "fixture-app", ["app_version"] = "0.0.0" },
            ["job"] = new Dictionary<string, object?> { ["target"] = "mock_target", ["command_type"] = "echo" },
        };

        await NewClient(transport).CreateJobAsync(body, new RequestOptions { IdempotencyKey = "idem_csharp_sdk" });

        var request = transport.LastRequest!;
        Assert.Equal("POST", request.Method);
        Assert.Equal("http://127.0.0.1:7878/v1/jobs", request.Url);
        Assert.Equal("idem_csharp_sdk", request.Headers["Idempotency-Key"]);
        Assert.Equal("application/json", request.Headers["Content-Type"]);

        using var sent = JsonDocument.Parse(request.Body!);
        var root = sent.RootElement;
        Assert.Equal(UbagClient.ApiVersion, root.GetProperty("api_version").GetString());
        Assert.Equal("idem_csharp_sdk", root.GetProperty("idempotency_key").GetString());
        var sdk = root.GetProperty("client").GetProperty("sdk");
        Assert.Equal(UbagClient.SdkName, sdk.GetProperty("name").GetString());
        Assert.Equal(UbagClient.SdkVersion, sdk.GetProperty("version").GetString());
    }

    [Fact]
    public async Task CreateJobGeneratesIdempotencyKeyWhenMissing()
    {
        var transport = new CapturingTransport(202, "{\"status\":\"queued\"}");
        var body = new Dictionary<string, object?>
        {
            ["job"] = new Dictionary<string, object?> { ["target"] = "mock_target" },
        };

        await NewClient(transport).CreateJobAsync(body);

        var request = transport.LastRequest!;
        var key = request.Headers["Idempotency-Key"];
        Assert.Equal(26, key.Length);
        using var sent = JsonDocument.Parse(request.Body!);
        Assert.Equal(key, sent.RootElement.GetProperty("idempotency_key").GetString());
    }

    [Fact]
    public async Task ListJobsBuildsFilterQuery()
    {
        var transport = new CapturingTransport(200, "{\"jobs\":[]}");
        await NewClient(transport).ListJobsAsync(new ListJobsParams
        {
            Cursor = "cursor_1",
            Limit = 1,
            Status = "completed",
        });

        Assert.Equal(
            "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed",
            transport.LastRequest!.Url);
    }

    [Fact]
    public async Task CancelJobIsIdempotentPost()
    {
        var transport = new CapturingTransport(202, "{\"status\":\"cancelled\"}");
        var request = new Dictionary<string, object?> { ["reason"] = "caller_cancelled" };
        await NewClient(transport).CancelJobAsync("job_1", request, new RequestOptions { IdempotencyKey = "idem_cancel" });

        var captured = transport.LastRequest!;
        Assert.Equal("POST", captured.Method);
        Assert.Equal("http://127.0.0.1:7878/v1/jobs/job_1/cancel", captured.Url);
        Assert.Equal("idem_cancel", captured.Headers["Idempotency-Key"]);
        using var sent = JsonDocument.Parse(captured.Body!);
        Assert.Equal("idem_cancel", sent.RootElement.GetProperty("idempotency_key").GetString());
        Assert.Equal("caller_cancelled", sent.RootElement.GetProperty("reason").GetString());
    }

    [Fact]
    public async Task PutArtifactSendsBytesAndGeneratesKey()
    {
        var transport = new CapturingTransport(201, "{\"idempotent_replay\":false}");
        var payload = Encoding.UTF8.GetBytes("hello artifact");
        await NewClient(transport).PutJobArtifactAsync("job_1", "report.txt", payload, "text/plain");

        var request = transport.LastRequest!;
        Assert.Equal("PUT", request.Method);
        Assert.Equal("http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt", request.Url);
        Assert.Equal("text/plain", request.Headers["Content-Type"]);
        Assert.Equal(26, request.Headers["Idempotency-Key"].Length);
        Assert.Equal(payload, request.Body);
    }

    [Fact]
    public async Task GetArtifactReturnsBytesAndChecksum()
    {
        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            ["content-type"] = "text/plain",
            ["ubag-artifact-checksum"] = "sha256_fixture",
        };
        var transport = new CapturingTransport(200, "hello artifact", headers);
        var download = await NewClient(transport).GetJobArtifactAsync("job_1", "report.txt");

        Assert.Equal("hello artifact", Encoding.UTF8.GetString(download.Body));
        Assert.Equal("text/plain", download.ContentType);
        Assert.Equal("sha256_fixture", download.Checksum);
    }

    [Fact]
    public async Task MetricsRequestSetsTextAccept()
    {
        var transport = new CapturingTransport(200, "ubag_gateway_requests_total 1\n");
        var text = await NewClient(transport).MetricsAsync();

        Assert.Equal("ubag_gateway_requests_total 1\n", text);
        Assert.Equal("text/plain", transport.LastRequest!.Headers["Accept"]);
    }

    [Fact]
    public async Task ApiErrorEnvelopeIsParsed()
    {
        var envelope = "{\"error\":{\"code\":\"UBAG-AUTH-MISSING-001\",\"category\":\"auth\","
            + "\"message\":\"No supported credential was provided\",\"retryable\":false,"
            + "\"trace_id\":\"trace_auth_missing\"}}";
        var transport = new CapturingTransport(401, envelope);

        var error = await Assert.ThrowsAsync<UbagApiException>(() => NewClient(transport).ListWorkflowsAsync());

        Assert.Equal(401, error.Status);
        Assert.Equal("UBAG-AUTH-MISSING-001", error.Code);
        Assert.Equal("auth", error.Category);
        Assert.False(error.Retryable);
        Assert.Equal("trace_auth_missing", error.TraceId);
        Assert.Contains("No supported credential", error.Message);
    }
}
