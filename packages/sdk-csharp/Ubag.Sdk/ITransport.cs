namespace Ubag.Sdk;

/// <summary>A single HTTP request to be sent by an <see cref="ITransport"/>.</summary>
public sealed record TransportRequest(
    string Method,
    string Url,
    IReadOnlyDictionary<string, string> Headers,
    byte[]? Body,
    string? ContentType);

/// <summary>A raw HTTP response captured by an <see cref="ITransport"/>.</summary>
public sealed record TransportResponse(
    int Status,
    IReadOnlyDictionary<string, string> Headers,
    byte[] Body);

/// <summary>
/// Pluggable HTTP transport. The default implementation uses <see cref="HttpClient"/>;
/// tests provide a capturing implementation to assert request construction.
/// </summary>
public interface ITransport
{
    Task<TransportResponse> SendAsync(TransportRequest request, CancellationToken cancellationToken = default);
}
