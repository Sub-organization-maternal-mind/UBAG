import Foundation

/// Per-call overrides for a request.
public struct RequestOptions {
    public var idempotencyKey: String?
    public var apiVersion: String?
    public var headers: [String: String]

    public init(idempotencyKey: String? = nil, apiVersion: String? = nil, headers: [String: String] = [:]) {
        self.idempotencyKey = idempotencyKey
        self.apiVersion = apiVersion
        self.headers = headers
    }
}

/// Cursor pagination parameters shared by operator list endpoints.
public struct ListParams {
    public var cursor: String?
    public var limit: Int?

    public init(cursor: String? = nil, limit: Int? = nil) {
        self.cursor = cursor
        self.limit = limit
    }
}

/// Filter and projection parameters for `GET /v1/jobs`.
public struct ListJobsParams {
    public var cursor: String?
    public var limit: Int?
    public var status: String?
    public var target: String?
    public var sort: String?
    public var fields: [String]?
    public var include: [String]?

    public init(
        cursor: String? = nil,
        limit: Int? = nil,
        status: String? = nil,
        target: String? = nil,
        sort: String? = nil,
        fields: [String]? = nil,
        include: [String]? = nil
    ) {
        self.cursor = cursor
        self.limit = limit
        self.status = status
        self.target = target
        self.sort = sort
        self.fields = fields
        self.include = include
    }
}

/// Pagination parameters for `GET /v1/jobs/{id}/events`.
public struct ListJobEventsParams {
    public var cursor: String?
    public var afterSequence: Int64?
    public var limit: Int?

    public init(cursor: String? = nil, afterSequence: Int64? = nil, limit: Int? = nil) {
        self.cursor = cursor
        self.afterSequence = afterSequence
        self.limit = limit
    }
}

/// A downloaded artifact: raw bytes plus content type and checksum.
public struct ArtifactDownload {
    public let body: Data
    public let contentType: String
    public let checksum: String
}

/// Swift client for the UBAG v0 REST gateway.
public final class UbagClient {
    public static let apiVersion = "2026-05-22"
    public static let sdkName = "ubag-swift"
    public static let sdkVersion = "0.0.0"

    private static let jsonContentType = "application/json"

    private static let queryAllowed: CharacterSet = {
        var set = CharacterSet.alphanumerics
        set.insert(charactersIn: "-._~")
        return set
    }()

    private let baseURL: String
    private let apiVersion: String
    private let appSecret: String?
    private let transport: Transport
    private let defaultHeaders: [String: String]

    public init(
        baseURL: String,
        appSecret: String? = nil,
        apiVersion: String = UbagClient.apiVersion,
        transport: Transport = URLSessionTransport(),
        defaultHeaders: [String: String] = [:]
    ) {
        let trimmed = baseURL.trimmingCharacters(in: .whitespaces)
        precondition(!trimmed.isEmpty, "baseURL is required")
        precondition(trimmed.contains("://"), "baseURL must include scheme and host")

        var normalized = trimmed
        while normalized.hasSuffix("/") {
            normalized.removeLast()
        }
        self.baseURL = normalized
        self.apiVersion = apiVersion
        self.appSecret = appSecret
        self.transport = transport
        self.defaultHeaders = defaultHeaders
    }

    // MARK: - System

    public func health(options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/health", body: nil, options: options)
    }

    public func ready(options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/ready", body: nil, options: options)
    }

    public func version(options: RequestOptions? = nil) async throws -> [String: Any] {
        var resolved = options ?? RequestOptions()
        resolved.idempotencyKey = nil
        return try await requestJSON("GET", "/v1/version", body: nil, options: resolved)
    }

    public func metrics(options: RequestOptions? = nil) async throws -> String {
        let resolved = withHeader(options, "Accept", "text/plain")
        let response = try await send("GET", "/v1/metrics", body: nil, contentType: nil, options: resolved)
        return String(decoding: response.body, as: UTF8.self)
    }

    // MARK: - Jobs

    public func createJob(_ request: [String: Any], options: RequestOptions? = nil) async throws -> [String: Any] {
        var body = request
        let resolvedVersion = stringField(body, "api_version") ?? options?.apiVersion ?? apiVersion
        let key = stringField(body, "idempotency_key") ?? options?.idempotencyKey ?? IdempotencyKey.generate()

        body["api_version"] = resolvedVersion
        body["idempotency_key"] = key
        ensureSdkMetadata(&body)

        let resolved = withIdempotency(options, apiVersion: resolvedVersion, key: key)
        return try await requestJSON("POST", "/v1/jobs", body: body, options: resolved)
    }

    public func getJob(_ jobId: String, options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/jobs/\(encode(jobId))", body: nil, options: options)
    }

    public func listJobs(_ params: ListJobsParams = ListJobsParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        var pairs: [(String, String)] = []
        addPair(&pairs, "cursor", params.cursor)
        addPair(&pairs, "limit", params.limit)
        addPair(&pairs, "filter[status]", params.status)
        addPair(&pairs, "filter[target]", params.target)
        addPair(&pairs, "sort", params.sort)
        addPair(&pairs, "fields", params.fields.map { $0.joined(separator: ",") })
        addPair(&pairs, "include", params.include.map { $0.joined(separator: ",") })

        return try await requestJSON("GET", "/v1/jobs" + encodeQuery(pairs), body: nil, options: options)
    }

    public func cancelJob(_ jobId: String, request: [String: Any] = [:], options: RequestOptions? = nil) async throws -> [String: Any] {
        try await mutate("/v1/jobs/\(encode(jobId))/cancel", request: request, options: options)
    }

    public func retryJob(_ jobId: String, request: [String: Any] = [:], options: RequestOptions? = nil) async throws -> [String: Any] {
        try await mutate("/v1/jobs/\(encode(jobId))/retry", request: request, options: options)
    }

    // MARK: - Job events

    public func listJobEvents(_ jobId: String, params: ListJobEventsParams = ListJobEventsParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        var pairs: [(String, String)] = []
        addPair(&pairs, "cursor", params.cursor)
        addPair(&pairs, "after_sequence", params.afterSequence)
        addPair(&pairs, "limit", params.limit)

        return try await requestJSON("GET", "/v1/jobs/\(encode(jobId))/events" + encodeQuery(pairs), body: nil, options: options)
    }

    public func streamJobEventsSSE(_ jobId: String, options: RequestOptions? = nil) async throws -> String {
        let resolved = withHeader(options, "Accept", "text/event-stream")
        let response = try await send("GET", "/v1/sse/jobs/\(encode(jobId))", body: nil, contentType: nil, options: resolved)
        return String(decoding: response.body, as: UTF8.self)
    }

    // MARK: - Artifacts

    public func listJobArtifacts(_ jobId: String, options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/jobs/\(encode(jobId))/artifacts", body: nil, options: options)
    }

    public func getJobArtifact(_ jobId: String, key: String, options: RequestOptions? = nil) async throws -> ArtifactDownload {
        let response = try await send("GET", "/v1/jobs/\(encode(jobId))/artifacts/\(encode(key))", body: nil, contentType: nil, options: options)
        return ArtifactDownload(
            body: response.body,
            contentType: response.headers["content-type"] ?? "",
            checksum: response.headers["ubag-artifact-checksum"] ?? ""
        )
    }

    @discardableResult
    public func putJobArtifact(
        _ jobId: String,
        key: String,
        body: Data,
        contentType: String = "application/octet-stream",
        options: RequestOptions? = nil
    ) async throws -> [String: Any] {
        let resolved = withIdempotency(options, apiVersion: options?.apiVersion ?? apiVersion, key: options?.idempotencyKey ?? IdempotencyKey.generate())
        let resolvedType = contentType.isEmpty ? "application/octet-stream" : contentType
        let response = try await send("PUT", "/v1/jobs/\(encode(jobId))/artifacts/\(encode(key))", body: body, contentType: resolvedType, options: resolved)
        return try decodeJSON(response.body)
    }

    public func deleteJobArtifact(_ jobId: String, key: String, options: RequestOptions? = nil) async throws {
        let resolved = withIdempotency(options, apiVersion: options?.apiVersion ?? apiVersion, key: options?.idempotencyKey ?? IdempotencyKey.generate())
        _ = try await send("DELETE", "/v1/jobs/\(encode(jobId))/artifacts/\(encode(key))", body: nil, contentType: nil, options: resolved)
    }

    // MARK: - Operator collections

    public func listWorkflows(options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/workflows", body: nil, options: options)
    }

    public func listTemplates(options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/templates", body: nil, options: options)
    }

    public func listTargets(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/targets" + buildListQuery(params), body: nil, options: options)
    }

    public func listAdapters(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/adapters" + buildListQuery(params), body: nil, options: options)
    }

    public func listApps(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/apps" + buildListQuery(params), body: nil, options: options)
    }

    public func listDevices(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/devices" + buildListQuery(params), body: nil, options: options)
    }

    public func listWebhooks(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/webhooks" + buildListQuery(params), body: nil, options: options)
    }

    public func listAuditEvents(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/audit" + buildListQuery(params), body: nil, options: options)
    }

    public func listEvents(_ params: ListParams = ListParams(), options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/events" + buildListQuery(params), body: nil, options: options)
    }

    public func replayWebhookDelivery(request: [String: Any] = [:], options: RequestOptions? = nil) async throws -> [String: Any] {
        try await mutate("/v1/webhooks/replay", request: request, options: options)
    }

    public func cacheStatus(options: RequestOptions? = nil) async throws -> [String: Any] {
        try await requestJSON("GET", "/v1/cache", body: nil, options: options)
    }

    // MARK: - Internal helpers

    private func mutate(_ path: String, request: [String: Any], options: RequestOptions?) async throws -> [String: Any] {
        var body = request
        let resolvedVersion = stringField(body, "api_version") ?? options?.apiVersion ?? apiVersion
        let key = stringField(body, "idempotency_key") ?? options?.idempotencyKey ?? IdempotencyKey.generate()

        body["api_version"] = resolvedVersion
        body["idempotency_key"] = key
        let resolved = withIdempotency(options, apiVersion: resolvedVersion, key: key)
        return try await requestJSON("POST", path, body: body, options: resolved)
    }

    private func requestJSON(_ method: String, _ path: String, body: [String: Any]?, options: RequestOptions?) async throws -> [String: Any] {
        var data: Data?
        var contentType: String?
        if let body = body {
            data = try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])
            contentType = Self.jsonContentType
        }

        let response = try await send(method, path, body: data, contentType: contentType, options: options)
        if response.body.isEmpty || response.status == 204 {
            return [:]
        }
        return try decodeJSON(response.body)
    }

    private func send(_ method: String, _ path: String, body: Data?, contentType: String?, options: RequestOptions?) async throws -> HTTPResponse {
        let url = baseURL + path
        let resolvedVersion = options?.apiVersion ?? apiVersion

        var headers: [String: String] = [
            "Accept": Self.jsonContentType,
            "Ubag-Api-Version": resolvedVersion,
            "Ubag-Sdk-Name": Self.sdkName,
            "Ubag-Sdk-Version": Self.sdkVersion,
        ]
        for (name, value) in defaultHeaders {
            headers[name] = value
        }
        if let optionHeaders = options?.headers {
            for (name, value) in optionHeaders {
                headers[name] = value
            }
        }
        if let appSecret = appSecret, headers["Authorization"] == nil {
            headers["Authorization"] = "Bearer \(appSecret)"
        }
        if let key = options?.idempotencyKey, !key.isEmpty {
            headers["Idempotency-Key"] = key
        }
        if body != nil {
            headers["Content-Type"] = contentType ?? Self.jsonContentType
        }

        let request = HTTPRequest(method: method, url: url, headers: headers, body: body)
        let response = try await transport.send(request)

        if response.status < 200 || response.status >= 300 {
            let rawBody = String(decoding: response.body, as: UTF8.self)
            throw UbagApiError(
                status: response.status,
                method: method,
                url: url,
                headers: response.headers,
                rawBody: rawBody,
                envelope: parseEnvelope(rawBody)
            )
        }

        return response
    }

    private func parseEnvelope(_ rawBody: String) -> [String: Any]? {
        guard !rawBody.isEmpty, let data = rawBody.data(using: .utf8),
              let parsed = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let error = parsed["error"] as? [String: Any],
              let code = error["code"] as? String, code.hasPrefix("UBAG-") else {
            return nil
        }
        return parsed
    }

    private func decodeJSON(_ data: Data) throws -> [String: Any] {
        let parsed = try JSONSerialization.jsonObject(with: data)
        return (parsed as? [String: Any]) ?? [:]
    }

    private func ensureSdkMetadata(_ body: inout [String: Any]) {
        let sdk: [String: Any] = ["name": Self.sdkName, "version": Self.sdkVersion]
        if var client = body["client"] as? [String: Any] {
            if client["sdk"] == nil {
                client["sdk"] = sdk
                body["client"] = client
            }
        } else {
            body["client"] = ["sdk": sdk]
        }
    }

    private func stringField(_ body: [String: Any], _ key: String) -> String? {
        guard let value = body[key] as? String, !value.isEmpty else { return nil }
        return value
    }

    private func withHeader(_ options: RequestOptions?, _ name: String, _ value: String) -> RequestOptions {
        var resolved = options ?? RequestOptions()
        resolved.headers[name] = value
        return resolved
    }

    private func withIdempotency(_ options: RequestOptions?, apiVersion: String, key: String) -> RequestOptions {
        var resolved = options ?? RequestOptions()
        resolved.apiVersion = apiVersion
        resolved.idempotencyKey = key
        return resolved
    }

    private func addPair(_ pairs: inout [(String, String)], _ key: String, _ value: String?) {
        guard let value = value, !value.isEmpty else { return }
        pairs.append((key, value))
    }

    private func addPair(_ pairs: inout [(String, String)], _ key: String, _ value: Int?) {
        guard let value = value, value > 0 else { return }
        pairs.append((key, String(value)))
    }

    private func addPair(_ pairs: inout [(String, String)], _ key: String, _ value: Int64?) {
        guard let value = value, value > 0 else { return }
        pairs.append((key, String(value)))
    }

    private func buildListQuery(_ params: ListParams) -> String {
        var pairs: [(String, String)] = []
        addPair(&pairs, "cursor", params.cursor)
        addPair(&pairs, "limit", params.limit)
        return encodeQuery(pairs)
    }

    private func encodeQuery(_ pairs: [(String, String)]) -> String {
        guard !pairs.isEmpty else { return "" }
        let encoded = pairs.map { "\(percentEncode($0.0))=\(percentEncode($0.1))" }
        return "?" + encoded.joined(separator: "&")
    }

    private func encode(_ value: String) -> String {
        percentEncode(value)
    }

    private func percentEncode(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: Self.queryAllowed) ?? value
    }
}
