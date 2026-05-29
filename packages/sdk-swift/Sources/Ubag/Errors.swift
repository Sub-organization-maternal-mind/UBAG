import Foundation

/// Thrown when the gateway returns a non-2xx response.
public struct UbagApiError: Error {
    public let status: Int
    public let method: String
    public let url: String
    public let headers: [String: String]
    public let rawBody: String
    public let envelope: [String: Any]?

    public init(
        status: Int,
        method: String,
        url: String,
        headers: [String: String],
        rawBody: String,
        envelope: [String: Any]?
    ) {
        self.status = status
        self.method = method
        self.url = url
        self.headers = headers
        self.rawBody = rawBody
        self.envelope = envelope
    }

    private func errorField(_ name: String) -> String? {
        guard let error = envelope?["error"] as? [String: Any],
              let value = error[name] as? String, !value.isEmpty else {
            return nil
        }
        return value
    }

    public var code: String? { errorField("code") }

    public var category: String? { errorField("category") }

    public var retryable: Bool {
        guard let error = envelope?["error"] as? [String: Any] else { return false }
        return (error["retryable"] as? Bool) ?? false
    }

    public var traceId: String? {
        if let fromEnvelope = errorField("trace_id") {
            return fromEnvelope
        }
        if let trace = headers["ubag-trace-id"], !trace.isEmpty {
            return trace
        }
        if let requestId = headers["x-request-id"], !requestId.isEmpty {
            return requestId
        }
        return nil
    }

    public var message: String {
        if let error = envelope?["error"] as? [String: Any],
           let message = error["message"] as? String, !message.isEmpty {
            return message
        }
        return "UBAG API request failed with HTTP \(status)"
    }
}

extension UbagApiError: CustomStringConvertible {
    public var description: String { message }
}

/// Thrown when a request could not be sent (network/transport failure).
public struct UbagTransportError: Error {
    public let method: String
    public let url: String
    public let underlying: Error

    public init(method: String, url: String, underlying: Error) {
        self.method = method
        self.url = url
        self.underlying = underlying
    }
}

extension UbagTransportError: CustomStringConvertible {
    public var description: String {
        "UBAG API request could not be sent: \(method) \(url): \(underlying)"
    }
}
