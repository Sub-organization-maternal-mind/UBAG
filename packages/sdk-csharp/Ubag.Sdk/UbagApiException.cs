using System.Text.Json;

namespace Ubag.Sdk;

/// <summary>Thrown when the gateway returns a non-2xx response.</summary>
public sealed class UbagApiException : Exception
{
    public UbagApiException(
        int status,
        string httpMethod,
        string url,
        IReadOnlyDictionary<string, string> headers,
        string rawBody,
        JsonElement? envelope)
        : base(MessageFor(status, envelope))
    {
        Status = status;
        HttpMethod = httpMethod;
        Url = url;
        Headers = headers;
        RawBody = rawBody;
        Envelope = envelope;
    }

    public int Status { get; }

    public string HttpMethod { get; }

    public string Url { get; }

    public IReadOnlyDictionary<string, string> Headers { get; }

    public string RawBody { get; }

    public JsonElement? Envelope { get; }

    public string? Code => ErrorField("code");

    public string? Category => ErrorField("category");

    public bool Retryable =>
        Envelope is { } envelope
        && envelope.TryGetProperty("error", out var error)
        && error.TryGetProperty("retryable", out var retryable)
        && retryable.ValueKind == JsonValueKind.True;

    public string? TraceId
    {
        get
        {
            var fromEnvelope = ErrorField("trace_id");
            if (!string.IsNullOrEmpty(fromEnvelope))
            {
                return fromEnvelope;
            }

            if (Headers.TryGetValue("ubag-trace-id", out var traceHeader) && traceHeader.Length > 0)
            {
                return traceHeader;
            }

            return Headers.TryGetValue("x-request-id", out var requestId) && requestId.Length > 0
                ? requestId
                : null;
        }
    }

    private string? ErrorField(string name)
    {
        if (Envelope is { } envelope
            && envelope.TryGetProperty("error", out var error)
            && error.TryGetProperty(name, out var field)
            && field.ValueKind == JsonValueKind.String)
        {
            return field.GetString();
        }

        return null;
    }

    private static string MessageFor(int status, JsonElement? envelope)
    {
        if (envelope is { } element
            && element.TryGetProperty("error", out var error)
            && error.TryGetProperty("message", out var message)
            && message.ValueKind == JsonValueKind.String)
        {
            var text = message.GetString();
            if (!string.IsNullOrEmpty(text))
            {
                return text;
            }
        }

        return $"UBAG API request failed with HTTP {status}";
    }
}
