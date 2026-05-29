namespace Ubag.Sdk;

/// <summary>Thrown when a request could not be sent (network/transport failure).</summary>
public sealed class UbagTransportException : Exception
{
    public UbagTransportException(string httpMethod, string url, Exception innerException)
        : base($"UBAG API request could not be sent: {httpMethod} {url}: {innerException.Message}", innerException)
    {
        HttpMethod = httpMethod;
        Url = url;
    }

    public string HttpMethod { get; }

    public string Url { get; }
}
