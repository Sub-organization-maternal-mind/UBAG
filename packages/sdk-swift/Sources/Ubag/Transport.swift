import Foundation

/// A single HTTP request to be sent by a ``Transport``.
public struct HTTPRequest: Sendable {
    public var method: String
    public var url: String
    public var headers: [String: String]
    public var body: Data?

    public init(method: String, url: String, headers: [String: String], body: Data?) {
        self.method = method
        self.url = url
        self.headers = headers
        self.body = body
    }
}

/// A raw HTTP response captured by a ``Transport``.
public struct HTTPResponse: Sendable {
    public var status: Int
    public var headers: [String: String]
    public var body: Data

    public init(status: Int, headers: [String: String], body: Data) {
        self.status = status
        self.headers = headers
        self.body = body
    }
}

/// Pluggable HTTP transport. The default implementation uses `URLSession`;
/// tests provide a capturing implementation to assert request construction.
public protocol Transport: Sendable {
    func send(_ request: HTTPRequest) async throws -> HTTPResponse
}

#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// Default ``Transport`` backed by `URLSession`.
public struct URLSessionTransport: Transport {
    private let session: URLSession

    public init(session: URLSession = .shared) {
        self.session = session
    }

    public func send(_ request: HTTPRequest) async throws -> HTTPResponse {
        guard let url = URL(string: request.url) else {
            throw UbagTransportError(method: request.method, url: request.url, underlying: URLError(.badURL))
        }

        var urlRequest = URLRequest(url: url)
        urlRequest.httpMethod = request.method
        for (name, value) in request.headers {
            urlRequest.setValue(value, forHTTPHeaderField: name)
        }
        urlRequest.httpBody = request.body

        let (data, response): (Data, URLResponse)
        do {
            (data, response) = try await session.data(for: urlRequest)
        } catch {
            throw UbagTransportError(method: request.method, url: request.url, underlying: error)
        }

        guard let httpResponse = response as? HTTPURLResponse else {
            throw UbagTransportError(method: request.method, url: request.url, underlying: URLError(.badServerResponse))
        }

        var headers: [String: String] = [:]
        for (key, value) in httpResponse.allHeaderFields {
            if let name = key as? String, let stringValue = value as? String {
                headers[name.lowercased()] = stringValue
            }
        }

        return HTTPResponse(status: httpResponse.statusCode, headers: headers, body: data)
    }
}
