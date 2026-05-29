using System.Text;
using System.Text.Json;

namespace Ubag.Sdk;

/// <summary>Per-call overrides for a request.</summary>
public sealed class RequestOptions
{
    public string? IdempotencyKey { get; set; }

    public string? ApiVersion { get; set; }

    public IDictionary<string, string> Headers { get; } = new Dictionary<string, string>();
}

/// <summary>Cursor pagination parameters shared by operator list endpoints.</summary>
public class ListParams
{
    public string? Cursor { get; set; }

    public int? Limit { get; set; }
}

/// <summary>Filter and projection parameters for <c>GET /v1/jobs</c>.</summary>
public sealed class ListJobsParams : ListParams
{
    public string? Status { get; set; }

    public string? Target { get; set; }

    public string? Sort { get; set; }

    public IReadOnlyList<string>? Fields { get; set; }

    public IReadOnlyList<string>? Include { get; set; }
}

/// <summary>Pagination parameters for <c>GET /v1/jobs/{id}/events</c>.</summary>
public sealed class ListJobEventsParams
{
    public string? Cursor { get; set; }

    public long? AfterSequence { get; set; }

    public int? Limit { get; set; }
}

/// <summary>A downloaded artifact: raw bytes plus content type and checksum.</summary>
public sealed record ArtifactDownload(byte[] Body, string ContentType, string Checksum);

/// <summary>Construction options for <see cref="UbagClient"/>.</summary>
public sealed class UbagClientOptions
{
    public string? AppSecret { get; set; }

    public string ApiVersion { get; set; } = UbagClient.ApiVersion;

    public ITransport? Transport { get; set; }

    public IDictionary<string, string> DefaultHeaders { get; } = new Dictionary<string, string>();
}

/// <summary>C# client for the UBAG v0 REST gateway.</summary>
public sealed class UbagClient
{
    public const string ApiVersion = "2026-05-22";
    public const string SdkName = "ubag-csharp";
    public const string SdkVersion = "0.0.0";

    private const string JsonContentType = "application/json";

    private static readonly JsonSerializerOptions SerializerOptions = new()
    {
        DefaultIgnoreCondition = System.Text.Json.Serialization.JsonIgnoreCondition.Never,
    };

    private readonly string _baseUrl;
    private readonly string _apiVersion;
    private readonly string? _appSecret;
    private readonly ITransport _transport;
    private readonly IReadOnlyDictionary<string, string> _defaultHeaders;

    public UbagClient(string baseUrl, UbagClientOptions? options = null)
    {
        if (string.IsNullOrWhiteSpace(baseUrl))
        {
            throw new ArgumentException("baseUrl is required", nameof(baseUrl));
        }

        baseUrl = baseUrl.Trim();
        if (!baseUrl.Contains("://", StringComparison.Ordinal))
        {
            throw new ArgumentException("baseUrl must include scheme and host", nameof(baseUrl));
        }

        options ??= new UbagClientOptions();
        _baseUrl = baseUrl.TrimEnd('/');
        _apiVersion = options.ApiVersion;
        _appSecret = options.AppSecret;
        _transport = options.Transport ?? new HttpClientTransport();
        _defaultHeaders = new Dictionary<string, string>(options.DefaultHeaders);
    }

    // --- System -----------------------------------------------------------

    public Task<JsonElement> HealthAsync(RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/health", null, options, ct);

    public Task<JsonElement> ReadyAsync(RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/ready", null, options, ct);

    public Task<JsonElement> VersionAsync(RequestOptions? options = null, CancellationToken ct = default)
    {
        if (options is not null)
        {
            options.IdempotencyKey = null;
        }

        return RequestJsonAsync("GET", "/v1/version", null, options, ct);
    }

    public async Task<string> MetricsAsync(RequestOptions? options = null, CancellationToken ct = default)
    {
        options = WithHeader(options, "Accept", "text/plain");
        var response = await SendAsync("GET", "/v1/metrics", null, null, options, ct).ConfigureAwait(false);
        return Encoding.UTF8.GetString(response.Body);
    }

    // --- Jobs --------------------------------------------------------------

    public Task<JsonElement> CreateJobAsync(IDictionary<string, object?> request, RequestOptions? options = null, CancellationToken ct = default)
    {
        var body = new Dictionary<string, object?>(request);
        var apiVersion = StringField(body, "api_version") ?? options?.ApiVersion ?? _apiVersion;
        var idempotencyKey = StringField(body, "idempotency_key")
            ?? options?.IdempotencyKey
            ?? Sdk.IdempotencyKey.Generate();

        body["api_version"] = apiVersion;
        body["idempotency_key"] = idempotencyKey;
        EnsureSdkMetadata(body);

        options = WithIdempotency(options, apiVersion, idempotencyKey);
        return RequestJsonAsync("POST", "/v1/jobs", body, options, ct);
    }

    public Task<JsonElement> GetJobAsync(string jobId, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", $"/v1/jobs/{Encode(jobId)}", null, options, ct);

    public Task<JsonElement> ListJobsAsync(ListJobsParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default)
    {
        var pairs = new List<(string, string)>();
        parameters ??= new ListJobsParams();
        AddPair(pairs, "cursor", parameters.Cursor);
        AddPair(pairs, "limit", parameters.Limit);
        AddPair(pairs, "filter[status]", parameters.Status);
        AddPair(pairs, "filter[target]", parameters.Target);
        AddPair(pairs, "sort", parameters.Sort);
        AddPair(pairs, "fields", parameters.Fields is null ? null : string.Join(",", parameters.Fields));
        AddPair(pairs, "include", parameters.Include is null ? null : string.Join(",", parameters.Include));

        return RequestJsonAsync("GET", "/v1/jobs" + EncodeQuery(pairs), null, options, ct);
    }

    public Task<JsonElement> CancelJobAsync(string jobId, IDictionary<string, object?>? request = null, RequestOptions? options = null, CancellationToken ct = default) =>
        MutateAsync($"/v1/jobs/{Encode(jobId)}/cancel", request, options, ct);

    public Task<JsonElement> RetryJobAsync(string jobId, IDictionary<string, object?>? request = null, RequestOptions? options = null, CancellationToken ct = default) =>
        MutateAsync($"/v1/jobs/{Encode(jobId)}/retry", request, options, ct);

    // --- Job events --------------------------------------------------------

    public Task<JsonElement> ListJobEventsAsync(string jobId, ListJobEventsParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default)
    {
        var pairs = new List<(string, string)>();
        parameters ??= new ListJobEventsParams();
        AddPair(pairs, "cursor", parameters.Cursor);
        AddPair(pairs, "after_sequence", parameters.AfterSequence);
        AddPair(pairs, "limit", parameters.Limit);

        return RequestJsonAsync("GET", $"/v1/jobs/{Encode(jobId)}/events" + EncodeQuery(pairs), null, options, ct);
    }

    public async Task<string> StreamJobEventsSseAsync(string jobId, RequestOptions? options = null, CancellationToken ct = default)
    {
        options = WithHeader(options, "Accept", "text/event-stream");
        var response = await SendAsync("GET", $"/v1/sse/jobs/{Encode(jobId)}", null, null, options, ct).ConfigureAwait(false);
        return Encoding.UTF8.GetString(response.Body);
    }

    // --- Artifacts ---------------------------------------------------------

    public Task<JsonElement> ListJobArtifactsAsync(string jobId, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", $"/v1/jobs/{Encode(jobId)}/artifacts", null, options, ct);

    public async Task<ArtifactDownload> GetJobArtifactAsync(string jobId, string key, RequestOptions? options = null, CancellationToken ct = default)
    {
        var response = await SendAsync("GET", $"/v1/jobs/{Encode(jobId)}/artifacts/{Encode(key)}", null, null, options, ct).ConfigureAwait(false);
        var contentType = response.Headers.TryGetValue("content-type", out var ct1) ? ct1 : string.Empty;
        var checksum = response.Headers.TryGetValue("ubag-artifact-checksum", out var sum) ? sum : string.Empty;
        return new ArtifactDownload(response.Body, contentType, checksum);
    }

    public async Task<JsonElement> PutJobArtifactAsync(
        string jobId,
        string key,
        byte[] body,
        string contentType = "application/octet-stream",
        RequestOptions? options = null,
        CancellationToken ct = default)
    {
        options = WithIdempotency(options, options?.ApiVersion ?? _apiVersion, options?.IdempotencyKey ?? Sdk.IdempotencyKey.Generate());
        var resolvedType = string.IsNullOrEmpty(contentType) ? "application/octet-stream" : contentType;
        var response = await SendAsync("PUT", $"/v1/jobs/{Encode(jobId)}/artifacts/{Encode(key)}", body, resolvedType, options, ct).ConfigureAwait(false);
        return ParseJson(response.Body);
    }

    public async Task DeleteJobArtifactAsync(string jobId, string key, RequestOptions? options = null, CancellationToken ct = default)
    {
        options = WithIdempotency(options, options?.ApiVersion ?? _apiVersion, options?.IdempotencyKey ?? Sdk.IdempotencyKey.Generate());
        await SendAsync("DELETE", $"/v1/jobs/{Encode(jobId)}/artifacts/{Encode(key)}", null, null, options, ct).ConfigureAwait(false);
    }

    // --- Operator collections ---------------------------------------------

    public Task<JsonElement> ListWorkflowsAsync(RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/workflows", null, options, ct);

    public Task<JsonElement> ListTemplatesAsync(RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/templates", null, options, ct);

    public Task<JsonElement> ListTargetsAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/targets" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListAdaptersAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/adapters" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListAppsAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/apps" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListDevicesAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/devices" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListWebhooksAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/webhooks" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListAuditEventsAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/audit" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ListEventsAsync(ListParams? parameters = null, RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/events" + BuildListQuery(parameters), null, options, ct);

    public Task<JsonElement> ReplayWebhookDeliveryAsync(IDictionary<string, object?>? request = null, RequestOptions? options = null, CancellationToken ct = default) =>
        MutateAsync("/v1/webhooks/replay", request, options, ct);

    public Task<JsonElement> CacheStatusAsync(RequestOptions? options = null, CancellationToken ct = default) =>
        RequestJsonAsync("GET", "/v1/cache", null, options, ct);

    // --- Internal helpers --------------------------------------------------

    private Task<JsonElement> MutateAsync(string path, IDictionary<string, object?>? request, RequestOptions? options, CancellationToken ct)
    {
        var body = request is null ? new Dictionary<string, object?>() : new Dictionary<string, object?>(request);
        var apiVersion = StringField(body, "api_version") ?? options?.ApiVersion ?? _apiVersion;
        var idempotencyKey = StringField(body, "idempotency_key")
            ?? options?.IdempotencyKey
            ?? Sdk.IdempotencyKey.Generate();

        body["api_version"] = apiVersion;
        body["idempotency_key"] = idempotencyKey;
        options = WithIdempotency(options, apiVersion, idempotencyKey);
        return RequestJsonAsync("POST", path, body, options, ct);
    }

    private async Task<JsonElement> RequestJsonAsync(string method, string path, IDictionary<string, object?>? body, RequestOptions? options, CancellationToken ct)
    {
        byte[]? serialized = null;
        string? contentType = null;
        if (body is not null)
        {
            serialized = JsonSerializer.SerializeToUtf8Bytes(body, SerializerOptions);
            contentType = JsonContentType;
        }

        var response = await SendAsync(method, path, serialized, contentType, options, ct).ConfigureAwait(false);
        if (response.Body.Length == 0 || response.Status == 204)
        {
            using var empty = JsonDocument.Parse("{}");
            return empty.RootElement.Clone();
        }

        return ParseJson(response.Body);
    }

    private async Task<TransportResponse> SendAsync(string method, string path, byte[]? body, string? contentType, RequestOptions? options, CancellationToken ct)
    {
        var url = _baseUrl + path;
        var apiVersion = options?.ApiVersion ?? _apiVersion;

        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            ["Accept"] = JsonContentType,
            ["Ubag-Api-Version"] = apiVersion,
            ["Ubag-Sdk-Name"] = SdkName,
            ["Ubag-Sdk-Version"] = SdkVersion,
        };

        foreach (var (name, value) in _defaultHeaders)
        {
            headers[name] = value;
        }

        if (options is not null)
        {
            foreach (var (name, value) in options.Headers)
            {
                headers[name] = value;
            }
        }

        if (_appSecret is not null && !headers.ContainsKey("Authorization"))
        {
            headers["Authorization"] = "Bearer " + _appSecret;
        }

        if (!string.IsNullOrEmpty(options?.IdempotencyKey))
        {
            headers["Idempotency-Key"] = options!.IdempotencyKey!;
        }

        if (body is not null)
        {
            headers["Content-Type"] = contentType ?? JsonContentType;
        }

        var request = new TransportRequest(method, url, headers, body, body is null ? null : contentType ?? JsonContentType);

        TransportResponse response;
        try
        {
            response = await _transport.SendAsync(request, ct).ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            throw new UbagTransportException(method, url, ex);
        }

        if (response.Status is < 200 or >= 300)
        {
            var rawBody = Encoding.UTF8.GetString(response.Body);
            throw new UbagApiException(response.Status, method, url, response.Headers, rawBody, ParseEnvelope(rawBody));
        }

        return response;
    }

    private static JsonElement ParseJson(byte[] body)
    {
        using var document = JsonDocument.Parse(body);
        return document.RootElement.Clone();
    }

    private static JsonElement? ParseEnvelope(string rawBody)
    {
        if (string.IsNullOrEmpty(rawBody))
        {
            return null;
        }

        try
        {
            using var document = JsonDocument.Parse(rawBody);
            var root = document.RootElement;
            if (root.ValueKind == JsonValueKind.Object
                && root.TryGetProperty("error", out var error)
                && error.TryGetProperty("code", out var code)
                && code.ValueKind == JsonValueKind.String
                && code.GetString() is { } codeValue
                && codeValue.StartsWith("UBAG-", StringComparison.Ordinal))
            {
                return root.Clone();
            }
        }
        catch (JsonException)
        {
            return null;
        }

        return null;
    }

    private static void EnsureSdkMetadata(IDictionary<string, object?> body)
    {
        if (body.TryGetValue("client", out var clientValue) && clientValue is IDictionary<string, object?> client)
        {
            if (!client.ContainsKey("sdk"))
            {
                client["sdk"] = new Dictionary<string, object?> { ["name"] = SdkName, ["version"] = SdkVersion };
            }

            return;
        }

        body["client"] = new Dictionary<string, object?>
        {
            ["sdk"] = new Dictionary<string, object?> { ["name"] = SdkName, ["version"] = SdkVersion },
        };
    }

    private static string? StringField(IDictionary<string, object?> body, string key) =>
        body.TryGetValue(key, out var value) && value is string text && text.Length > 0 ? text : null;

    private static RequestOptions WithHeader(RequestOptions? options, string name, string value)
    {
        options ??= new RequestOptions();
        options.Headers[name] = value;
        return options;
    }

    private static RequestOptions WithIdempotency(RequestOptions? options, string apiVersion, string idempotencyKey)
    {
        options ??= new RequestOptions();
        options.ApiVersion = apiVersion;
        options.IdempotencyKey = idempotencyKey;
        return options;
    }

    private static void AddPair(ICollection<(string, string)> pairs, string key, string? value)
    {
        if (!string.IsNullOrEmpty(value))
        {
            pairs.Add((key, value));
        }
    }

    private static void AddPair(ICollection<(string, string)> pairs, string key, int? value)
    {
        if (value is > 0)
        {
            pairs.Add((key, value.Value.ToString(System.Globalization.CultureInfo.InvariantCulture)));
        }
    }

    private static void AddPair(ICollection<(string, string)> pairs, string key, long? value)
    {
        if (value is > 0)
        {
            pairs.Add((key, value.Value.ToString(System.Globalization.CultureInfo.InvariantCulture)));
        }
    }

    private static string BuildListQuery(ListParams? parameters)
    {
        var pairs = new List<(string, string)>();
        if (parameters is not null)
        {
            AddPair(pairs, "cursor", parameters.Cursor);
            AddPair(pairs, "limit", parameters.Limit);
        }

        return EncodeQuery(pairs);
    }

    private static string EncodeQuery(IReadOnlyList<(string Key, string Value)> pairs)
    {
        if (pairs.Count == 0)
        {
            return string.Empty;
        }

        var encoded = pairs.Select(pair => $"{Uri.EscapeDataString(pair.Key)}={Uri.EscapeDataString(pair.Value)}");
        return "?" + string.Join("&", encoded);
    }

    private static string Encode(string value) => Uri.EscapeDataString(value);
}
