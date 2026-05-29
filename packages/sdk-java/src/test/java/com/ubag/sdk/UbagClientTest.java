package com.ubag.sdk;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class UbagClientTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static final class CapturingTransport implements Transport {
        final AtomicReference<Request> last = new AtomicReference<>();
        final int status;
        final byte[] body;
        final Map<String, String> headers;

        CapturingTransport(int status, byte[] body, Map<String, String> headers) {
            this.status = status;
            this.body = body;
            this.headers = headers;
        }

        static CapturingTransport json(int status, String body) {
            return new CapturingTransport(status, body.getBytes(StandardCharsets.UTF_8), new LinkedHashMap<>());
        }

        @Override
        public Response execute(Request request) {
            last.set(request);
            return new Response(status, headers, body);
        }
    }

    private UbagClient client(CapturingTransport transport) {
        return UbagClient.builder("http://127.0.0.1:7878/")
                .appSecret("app_secret_fixture")
                .transport(transport)
                .build();
    }

    @Test
    void healthSendsVersionAndAuthHeaders() {
        CapturingTransport transport = CapturingTransport.json(200, "{\"status\":\"ok\"}");
        Map<String, Object> result = client(transport).health(UbagClient.RequestOptions.create());

        assertEquals("ok", result.get("status"));
        Transport.Request request = transport.last.get();
        assertEquals("GET", request.method);
        assertEquals("http://127.0.0.1:7878/v1/health", request.url);
        assertEquals(UbagClient.API_VERSION, request.headers.get("Ubag-Api-Version"));
        assertEquals(UbagClient.SDK_NAME, request.headers.get("Ubag-Sdk-Name"));
        assertEquals(UbagClient.SDK_VERSION, request.headers.get("Ubag-Sdk-Version"));
        assertEquals("Bearer app_secret_fixture", request.headers.get("Authorization"));
        assertNull(request.body);
    }

    @Test
    void versionOmitsIdempotencyKey() {
        CapturingTransport transport = CapturingTransport.json(200, "{\"version\":\"0.0.0\"}");
        client(transport).version(UbagClient.RequestOptions.create().idempotencyKey("ignored"));

        Transport.Request request = transport.last.get();
        assertEquals("http://127.0.0.1:7878/v1/version", request.url);
        assertNull(request.headers.get("Idempotency-Key"));
    }

    @Test
    void createJobInjectsVersionIdempotencyAndSdkMetadata() throws Exception {
        CapturingTransport transport = CapturingTransport.json(202, "{\"status\":\"queued\"}");
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("client", Map.of("app_id", "fixture-app", "app_version", "0.0.0"));
        body.put("job", Map.of("target", "mock_target", "command_type", "echo"));

        client(transport).createJob(body, UbagClient.RequestOptions.create().idempotencyKey("idem_java_sdk"));

        Transport.Request request = transport.last.get();
        assertEquals("POST", request.method);
        assertEquals("http://127.0.0.1:7878/v1/jobs", request.url);
        assertEquals("idem_java_sdk", request.headers.get("Idempotency-Key"));
        assertEquals("application/json", request.headers.get("Content-Type"));

        Map<?, ?> sent = MAPPER.readValue(request.body, Map.class);
        assertEquals(UbagClient.API_VERSION, sent.get("api_version"));
        assertEquals("idem_java_sdk", sent.get("idempotency_key"));
        Map<?, ?> clientMeta = (Map<?, ?>) sent.get("client");
        Map<?, ?> sdk = (Map<?, ?>) clientMeta.get("sdk");
        assertEquals(UbagClient.SDK_NAME, sdk.get("name"));
        assertEquals(UbagClient.SDK_VERSION, sdk.get("version"));
    }

    @Test
    void createJobGeneratesIdempotencyKeyWhenMissing() throws Exception {
        CapturingTransport transport = CapturingTransport.json(202, "{\"status\":\"queued\"}");
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("job", Map.of("target", "mock_target"));

        client(transport).createJob(body, UbagClient.RequestOptions.create());

        Transport.Request request = transport.last.get();
        String key = request.headers.get("Idempotency-Key");
        assertEquals(26, key.length());
        Map<?, ?> sent = MAPPER.readValue(request.body, Map.class);
        assertEquals(key, sent.get("idempotency_key"));
    }

    @Test
    void listJobsBuildsFilterQuery() {
        CapturingTransport transport = CapturingTransport.json(200, "{\"jobs\":[]}");
        UbagClient.ListJobsParams params = new UbagClient.ListJobsParams();
        params.cursor = "cursor_1";
        params.limit = 1;
        params.status = "completed";

        client(transport).listJobs(params, UbagClient.RequestOptions.create());

        assertEquals(
                "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed",
                transport.last.get().url);
    }

    @Test
    void cancelJobIsIdempotentPost() throws Exception {
        CapturingTransport transport = CapturingTransport.json(202, "{\"status\":\"cancelled\"}");
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("reason", "caller_cancelled");

        client(transport).cancelJob("job_1", body, UbagClient.RequestOptions.create().idempotencyKey("idem_cancel"));

        Transport.Request request = transport.last.get();
        assertEquals("POST", request.method);
        assertEquals("http://127.0.0.1:7878/v1/jobs/job_1/cancel", request.url);
        assertEquals("idem_cancel", request.headers.get("Idempotency-Key"));
        Map<?, ?> sent = MAPPER.readValue(request.body, Map.class);
        assertEquals("idem_cancel", sent.get("idempotency_key"));
        assertEquals("caller_cancelled", sent.get("reason"));
    }

    @Test
    void putArtifactSendsBytesAndGeneratesKey() {
        CapturingTransport transport = CapturingTransport.json(201, "{\"idempotent_replay\":false}");
        client(transport).putJobArtifact(
                "job_1", "report.txt", "hello artifact".getBytes(StandardCharsets.UTF_8), "text/plain",
                UbagClient.RequestOptions.create());

        Transport.Request request = transport.last.get();
        assertEquals("PUT", request.method);
        assertEquals("http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt", request.url);
        assertEquals("text/plain", request.headers.get("Content-Type"));
        assertEquals(26, request.headers.get("Idempotency-Key").length());
        assertEquals("hello artifact", new String(request.body, StandardCharsets.UTF_8));
    }

    @Test
    void getArtifactReturnsBytesAndChecksum() {
        Map<String, String> headers = new LinkedHashMap<>();
        headers.put("content-type", "text/plain");
        headers.put("ubag-artifact-checksum", "sha256_fixture");
        CapturingTransport transport =
                new CapturingTransport(200, "hello artifact".getBytes(StandardCharsets.UTF_8), headers);

        UbagClient.ArtifactDownload download =
                client(transport).getJobArtifact("job_1", "report.txt", UbagClient.RequestOptions.create());

        assertEquals("hello artifact", new String(download.body, StandardCharsets.UTF_8));
        assertEquals("text/plain", download.contentType);
        assertEquals("sha256_fixture", download.checksum);
    }

    @Test
    void metricsRequestSetsTextAccept() {
        CapturingTransport transport = new CapturingTransport(
                200, "ubag_gateway_requests_total 1\n".getBytes(StandardCharsets.UTF_8), new LinkedHashMap<>());
        String text = client(transport).metrics(UbagClient.RequestOptions.create());

        assertEquals("ubag_gateway_requests_total 1\n", text);
        assertEquals("text/plain", transport.last.get().headers.get("Accept"));
    }

    @Test
    void apiErrorEnvelopeIsParsed() {
        CapturingTransport transport = CapturingTransport.json(
                401,
                "{\"error\":{\"code\":\"UBAG-AUTH-MISSING-001\",\"category\":\"auth\","
                        + "\"message\":\"No supported credential was provided\",\"retryable\":false,"
                        + "\"trace_id\":\"trace_auth_missing\"}}");

        UbagApiException error = assertThrows(
                UbagApiException.class,
                () -> client(transport).listWorkflows(UbagClient.RequestOptions.create()));

        assertEquals(401, error.status());
        assertEquals("UBAG-AUTH-MISSING-001", error.code());
        assertEquals("auth", error.category());
        assertFalse(error.retryable());
        assertEquals("trace_auth_missing", error.traceId());
        assertTrue(error.getMessage().contains("No supported credential"));
    }
}
