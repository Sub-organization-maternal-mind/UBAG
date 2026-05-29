import Foundation
import XCTest
@testable import Ubag

/// Capturing transport that records the last request and returns a canned response.
final class CapturingTransport: Transport, @unchecked Sendable {
    private let status: Int
    private let responseBody: Data
    private let responseHeaders: [String: String]
    private(set) var lastRequest: HTTPRequest?

    init(status: Int, body: String, headers: [String: String] = [:]) {
        self.status = status
        self.responseBody = Data(body.utf8)
        self.responseHeaders = headers
    }

    func send(_ request: HTTPRequest) async throws -> HTTPResponse {
        lastRequest = request
        return HTTPResponse(status: status, headers: responseHeaders, body: responseBody)
    }
}

final class UbagClientTests: XCTestCase {
    private func makeClient(_ transport: Transport) -> UbagClient {
        UbagClient(baseURL: "http://127.0.0.1:7878/", appSecret: "app_secret_fixture", transport: transport)
    }

    func testHealthSendsVersionAndAuthHeaders() async throws {
        let transport = CapturingTransport(status: 200, body: "{\"status\":\"ok\"}")
        let result = try await makeClient(transport).health()

        XCTAssertEqual(result["status"] as? String, "ok")
        let request = try XCTUnwrap(transport.lastRequest)
        XCTAssertEqual(request.method, "GET")
        XCTAssertEqual(request.url, "http://127.0.0.1:7878/v1/health")
        XCTAssertEqual(request.headers["Ubag-Api-Version"], UbagClient.apiVersion)
        XCTAssertEqual(request.headers["Ubag-Sdk-Name"], UbagClient.sdkName)
        XCTAssertEqual(request.headers["Authorization"], "Bearer app_secret_fixture")
        XCTAssertNil(request.body)
    }

    func testVersionOmitsIdempotencyKey() async throws {
        let transport = CapturingTransport(status: 200, body: "{\"version\":\"0.0.0\"}")
        let options = RequestOptions(idempotencyKey: "ignored")
        _ = try await makeClient(transport).version(options: options)

        let request = try XCTUnwrap(transport.lastRequest)
        XCTAssertEqual(request.url, "http://127.0.0.1:7878/v1/version")
        XCTAssertNil(request.headers["Idempotency-Key"])
    }

    func testCreateJobInjectsVersionIdempotencyAndSdkMetadata() async throws {
        let transport = CapturingTransport(status: 202, body: "{\"status\":\"queued\"}")
        let body: [String: Any] = [
            "client": ["app_id": "fixture-app", "app_version": "0.0.0"],
            "job": ["target": "mock_target", "command_type": "echo"],
        ]

        _ = try await makeClient(transport).createJob(body, options: RequestOptions(idempotencyKey: "idem_swift_sdk"))

        let request = try XCTUnwrap(transport.lastRequest)
        XCTAssertEqual(request.method, "POST")
        XCTAssertEqual(request.url, "http://127.0.0.1:7878/v1/jobs")
        XCTAssertEqual(request.headers["Idempotency-Key"], "idem_swift_sdk")
        XCTAssertEqual(request.headers["Content-Type"], "application/json")

        let sent = try JSONSerialization.jsonObject(with: try XCTUnwrap(request.body)) as? [String: Any]
        XCTAssertEqual(sent?["api_version"] as? String, UbagClient.apiVersion)
        XCTAssertEqual(sent?["idempotency_key"] as? String, "idem_swift_sdk")
        let sdk = (sent?["client"] as? [String: Any])?["sdk"] as? [String: Any]
        XCTAssertEqual(sdk?["name"] as? String, UbagClient.sdkName)
        XCTAssertEqual(sdk?["version"] as? String, UbagClient.sdkVersion)
    }

    func testCreateJobGeneratesIdempotencyKeyWhenMissing() async throws {
        let transport = CapturingTransport(status: 202, body: "{\"status\":\"queued\"}")
        _ = try await makeClient(transport).createJob(["job": ["target": "mock_target"]])

        let request = try XCTUnwrap(transport.lastRequest)
        let key = try XCTUnwrap(request.headers["Idempotency-Key"])
        XCTAssertEqual(key.count, 26)
        let sent = try JSONSerialization.jsonObject(with: try XCTUnwrap(request.body)) as? [String: Any]
        XCTAssertEqual(sent?["idempotency_key"] as? String, key)
    }

    func testListJobsBuildsFilterQuery() async throws {
        let transport = CapturingTransport(status: 200, body: "{\"jobs\":[]}")
        _ = try await makeClient(transport).listJobs(ListJobsParams(cursor: "cursor_1", limit: 1, status: "completed"))

        XCTAssertEqual(
            transport.lastRequest?.url,
            "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed"
        )
    }

    func testCancelJobIsIdempotentPost() async throws {
        let transport = CapturingTransport(status: 202, body: "{\"status\":\"cancelled\"}")
        _ = try await makeClient(transport).cancelJob(
            "job_1",
            request: ["reason": "caller_cancelled"],
            options: RequestOptions(idempotencyKey: "idem_cancel")
        )

        let request = try XCTUnwrap(transport.lastRequest)
        XCTAssertEqual(request.method, "POST")
        XCTAssertEqual(request.url, "http://127.0.0.1:7878/v1/jobs/job_1/cancel")
        XCTAssertEqual(request.headers["Idempotency-Key"], "idem_cancel")
        let sent = try JSONSerialization.jsonObject(with: try XCTUnwrap(request.body)) as? [String: Any]
        XCTAssertEqual(sent?["idempotency_key"] as? String, "idem_cancel")
        XCTAssertEqual(sent?["reason"] as? String, "caller_cancelled")
    }

    func testPutArtifactSendsBytesAndGeneratesKey() async throws {
        let transport = CapturingTransport(status: 201, body: "{\"idempotent_replay\":false}")
        let payload = Data("hello artifact".utf8)
        _ = try await makeClient(transport).putJobArtifact("job_1", key: "report.txt", body: payload, contentType: "text/plain")

        let request = try XCTUnwrap(transport.lastRequest)
        XCTAssertEqual(request.method, "PUT")
        XCTAssertEqual(request.url, "http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt")
        XCTAssertEqual(request.headers["Content-Type"], "text/plain")
        XCTAssertEqual(request.headers["Idempotency-Key"]?.count, 26)
        XCTAssertEqual(request.body, payload)
    }

    func testGetArtifactReturnsBytesAndChecksum() async throws {
        let transport = CapturingTransport(
            status: 200,
            body: "hello artifact",
            headers: ["content-type": "text/plain", "ubag-artifact-checksum": "sha256_fixture"]
        )
        let download = try await makeClient(transport).getJobArtifact("job_1", key: "report.txt")

        XCTAssertEqual(String(decoding: download.body, as: UTF8.self), "hello artifact")
        XCTAssertEqual(download.contentType, "text/plain")
        XCTAssertEqual(download.checksum, "sha256_fixture")
    }

    func testMetricsRequestSetsTextAccept() async throws {
        let transport = CapturingTransport(status: 200, body: "ubag_gateway_requests_total 1\n")
        let text = try await makeClient(transport).metrics()

        XCTAssertEqual(text, "ubag_gateway_requests_total 1\n")
        XCTAssertEqual(transport.lastRequest?.headers["Accept"], "text/plain")
    }

    func testApiErrorEnvelopeIsParsed() async throws {
        let envelope = "{\"error\":{\"code\":\"UBAG-AUTH-MISSING-001\",\"category\":\"auth\","
            + "\"message\":\"No supported credential was provided\",\"retryable\":false,"
            + "\"trace_id\":\"trace_auth_missing\"}}"
        let transport = CapturingTransport(status: 401, body: envelope)

        do {
            _ = try await makeClient(transport).listWorkflows()
            XCTFail("expected UbagApiError")
        } catch let error as UbagApiError {
            XCTAssertEqual(error.status, 401)
            XCTAssertEqual(error.code, "UBAG-AUTH-MISSING-001")
            XCTAssertEqual(error.category, "auth")
            XCTAssertFalse(error.retryable)
            XCTAssertEqual(error.traceId, "trace_auth_missing")
            XCTAssertTrue(error.message.contains("No supported credential"))
        }
    }
}
