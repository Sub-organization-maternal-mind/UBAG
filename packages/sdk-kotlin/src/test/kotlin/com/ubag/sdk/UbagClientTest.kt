package com.ubag.sdk

import org.json.JSONObject
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Assertions.assertThrows
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test
import java.nio.charset.StandardCharsets

/** Capturing transport that records the last request and returns a canned response. */
class CapturingTransport(
    private val status: Int,
    body: String,
    private val responseHeaders: Map<String, String> = emptyMap(),
) : Transport {
    private val responseBody = body.toByteArray(StandardCharsets.UTF_8)
    var lastRequest: TransportRequest? = null
        private set

    override fun send(request: TransportRequest): TransportResponse {
        lastRequest = request
        return TransportResponse(status, responseHeaders, responseBody)
    }
}

class UbagClientTest {
    private fun client(transport: Transport): UbagClient =
        UbagClient("http://127.0.0.1:7878/", appSecret = "app_secret_fixture", transport = transport)

    private fun bodyJson(request: TransportRequest): JSONObject =
        JSONObject(String(request.body!!, StandardCharsets.UTF_8))

    @Test
    fun healthSendsVersionAndAuthHeaders() {
        val transport = CapturingTransport(200, "{\"status\":\"ok\"}")
        val result = client(transport).health()

        assertEquals("ok", result.getString("status"))
        val request = transport.lastRequest!!
        assertEquals("GET", request.method)
        assertEquals("http://127.0.0.1:7878/v1/health", request.url)
        assertEquals(UbagClient.API_VERSION, request.headers["Ubag-Api-Version"])
        assertEquals(UbagClient.SDK_NAME, request.headers["Ubag-Sdk-Name"])
        assertEquals("Bearer app_secret_fixture", request.headers["Authorization"])
        assertNull(request.body)
    }

    @Test
    fun versionOmitsIdempotencyKey() {
        val transport = CapturingTransport(200, "{\"version\":\"0.0.0\"}")
        client(transport).version(RequestOptions(idempotencyKey = "ignored"))

        val request = transport.lastRequest!!
        assertEquals("http://127.0.0.1:7878/v1/version", request.url)
        assertNull(request.headers["Idempotency-Key"])
    }

    @Test
    fun createJobInjectsVersionIdempotencyAndSdkMetadata() {
        val transport = CapturingTransport(202, "{\"status\":\"queued\"}")
        val body = mapOf(
            "client" to mapOf("app_id" to "fixture-app", "app_version" to "0.0.0"),
            "job" to mapOf("target" to "mock_target", "command_type" to "echo"),
        )

        client(transport).createJob(body, RequestOptions(idempotencyKey = "idem_kotlin_sdk"))

        val request = transport.lastRequest!!
        assertEquals("POST", request.method)
        assertEquals("http://127.0.0.1:7878/v1/jobs", request.url)
        assertEquals("idem_kotlin_sdk", request.headers["Idempotency-Key"])
        assertEquals("application/json", request.headers["Content-Type"])

        val sent = bodyJson(request)
        assertEquals(UbagClient.API_VERSION, sent.getString("api_version"))
        assertEquals("idem_kotlin_sdk", sent.getString("idempotency_key"))
        val sdk = sent.getJSONObject("client").getJSONObject("sdk")
        assertEquals(UbagClient.SDK_NAME, sdk.getString("name"))
        assertEquals(UbagClient.SDK_VERSION, sdk.getString("version"))
    }

    @Test
    fun createJobGeneratesIdempotencyKeyWhenMissing() {
        val transport = CapturingTransport(202, "{\"status\":\"queued\"}")
        client(transport).createJob(mapOf("job" to mapOf("target" to "mock_target")))

        val request = transport.lastRequest!!
        val key = request.headers["Idempotency-Key"]!!
        assertEquals(26, key.length)
        assertEquals(key, bodyJson(request).getString("idempotency_key"))
    }

    @Test
    fun listJobsBuildsFilterQuery() {
        val transport = CapturingTransport(200, "{\"jobs\":[]}")
        client(transport).listJobs(ListJobsParams(cursor = "cursor_1", limit = 1, status = "completed"))

        assertEquals(
            "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed",
            transport.lastRequest!!.url,
        )
    }

    @Test
    fun cancelJobIsIdempotentPost() {
        val transport = CapturingTransport(202, "{\"status\":\"cancelled\"}")
        client(transport).cancelJob("job_1", mapOf("reason" to "caller_cancelled"), RequestOptions(idempotencyKey = "idem_cancel"))

        val request = transport.lastRequest!!
        assertEquals("POST", request.method)
        assertEquals("http://127.0.0.1:7878/v1/jobs/job_1/cancel", request.url)
        assertEquals("idem_cancel", request.headers["Idempotency-Key"])
        val sent = bodyJson(request)
        assertEquals("idem_cancel", sent.getString("idempotency_key"))
        assertEquals("caller_cancelled", sent.getString("reason"))
    }

    @Test
    fun putArtifactSendsBytesAndGeneratesKey() {
        val transport = CapturingTransport(201, "{\"idempotent_replay\":false}")
        val payload = "hello artifact".toByteArray(StandardCharsets.UTF_8)
        client(transport).putJobArtifact("job_1", "report.txt", payload, "text/plain")

        val request = transport.lastRequest!!
        assertEquals("PUT", request.method)
        assertEquals("http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt", request.url)
        assertEquals("text/plain", request.headers["Content-Type"])
        assertEquals(26, request.headers["Idempotency-Key"]!!.length)
        assertTrue(payload.contentEquals(request.body))
    }

    @Test
    fun getArtifactReturnsBytesAndChecksum() {
        val transport = CapturingTransport(
            200,
            "hello artifact",
            mapOf("content-type" to "text/plain", "ubag-artifact-checksum" to "sha256_fixture"),
        )
        val download = client(transport).getJobArtifact("job_1", "report.txt")

        assertEquals("hello artifact", String(download.body, StandardCharsets.UTF_8))
        assertEquals("text/plain", download.contentType)
        assertEquals("sha256_fixture", download.checksum)
    }

    @Test
    fun metricsRequestSetsTextAccept() {
        val transport = CapturingTransport(200, "ubag_gateway_requests_total 1\n")
        val text = client(transport).metrics()

        assertEquals("ubag_gateway_requests_total 1\n", text)
        assertEquals("text/plain", transport.lastRequest!!.headers["Accept"])
    }

    @Test
    fun apiErrorEnvelopeIsParsed() {
        val envelope = """
            {"error":{"code":"UBAG-AUTH-MISSING-001","category":"auth",
            "message":"No supported credential was provided","retryable":false,
            "trace_id":"trace_auth_missing"}}
        """.trimIndent()
        val transport = CapturingTransport(401, envelope)

        val error = assertThrows(UbagApiException::class.java) {
            client(transport).listWorkflows()
        }

        assertEquals(401, error.status)
        assertEquals("UBAG-AUTH-MISSING-001", error.code())
        assertEquals("auth", error.category())
        assertFalse(error.retryable())
        assertEquals("trace_auth_missing", error.traceId())
        assertTrue(error.message!!.contains("No supported credential"))
    }
}
