package com.ubag.sdk;

import com.fasterxml.jackson.databind.ObjectMapper;

import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Java client for the UBAG v0 REST gateway.
 *
 * <p>The client builds requests via a pluggable {@link Transport} so request
 * construction can be validated without a live gateway.
 */
public final class UbagClient {

    public static final String API_VERSION = "2026-05-22";
    public static final String SDK_NAME = "ubag-java";
    public static final String SDK_VERSION = "0.0.0";

    private static final String JSON_CONTENT_TYPE = "application/json";

    private final String baseUrl;
    private final String apiVersion;
    private final String appSecret;
    private final Map<String, String> defaultHeaders;
    private final Transport transport;
    private final ObjectMapper mapper = new ObjectMapper();

    private UbagClient(Builder builder) {
        String normalized = builder.baseUrl == null ? "" : builder.baseUrl.trim();
        if (normalized.isEmpty()) {
            throw new IllegalArgumentException("baseUrl is required");
        }
        if (!normalized.contains("://")) {
            throw new IllegalArgumentException("baseUrl must include scheme and host");
        }
        while (normalized.endsWith("/")) {
            normalized = normalized.substring(0, normalized.length() - 1);
        }
        this.baseUrl = normalized;
        this.apiVersion = builder.apiVersion == null ? API_VERSION : builder.apiVersion;
        this.appSecret = builder.appSecret;
        this.defaultHeaders = new LinkedHashMap<>(builder.defaultHeaders);
        this.transport = builder.transport == null ? new JdkHttpTransport() : builder.transport;
    }

    public static Builder builder(String baseUrl) {
        return new Builder(baseUrl);
    }

    /** Builder for {@link UbagClient}. */
    public static final class Builder {
        private final String baseUrl;
        private String apiVersion = API_VERSION;
        private String appSecret;
        private Transport transport;
        private final Map<String, String> defaultHeaders = new LinkedHashMap<>();

        public Builder(String baseUrl) {
            this.baseUrl = baseUrl;
        }

        public Builder appSecret(String appSecret) {
            this.appSecret = appSecret;
            return this;
        }

        public Builder apiVersion(String apiVersion) {
            this.apiVersion = apiVersion;
            return this;
        }

        public Builder transport(Transport transport) {
            this.transport = transport;
            return this;
        }

        public Builder defaultHeader(String key, String value) {
            this.defaultHeaders.put(key, value);
            return this;
        }

        public UbagClient build() {
            return new UbagClient(this);
        }
    }

    /** Per-request overrides. */
    public static final class RequestOptions {
        String apiVersion;
        String idempotencyKey;
        final Map<String, String> headers = new LinkedHashMap<>();

        public static RequestOptions create() {
            return new RequestOptions();
        }

        public RequestOptions apiVersion(String value) {
            this.apiVersion = value;
            return this;
        }

        public RequestOptions idempotencyKey(String value) {
            this.idempotencyKey = value;
            return this;
        }

        public RequestOptions header(String key, String value) {
            this.headers.put(key, value);
            return this;
        }
    }

    // --- System -----------------------------------------------------------

    public Map<String, Object> health(RequestOptions options) {
        return requestJson("GET", "/v1/health", null, options);
    }

    public Map<String, Object> ready(RequestOptions options) {
        return requestJson("GET", "/v1/ready", null, options);
    }

    public Map<String, Object> version(RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        effective.idempotencyKey = null;
        return requestJson("GET", "/v1/version", null, effective);
    }

    public String metrics(RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        effective.headers.put("Accept", "text/plain");
        Transport.Response response = send("GET", "/v1/metrics", null, null, effective);
        return new String(response.body, StandardCharsets.UTF_8);
    }

    // --- Jobs --------------------------------------------------------------

    public Map<String, Object> createJob(Map<String, Object> request, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        Map<String, Object> body = new LinkedHashMap<>(request);

        String apiVersion = stringField(body, "api_version");
        if (apiVersion == null) {
            apiVersion = effective.apiVersion != null ? effective.apiVersion : this.apiVersion;
        }
        String idempotencyKey = stringField(body, "idempotency_key");
        if (idempotencyKey == null) {
            idempotencyKey = effective.idempotencyKey != null ? effective.idempotencyKey : IdempotencyKey.generate();
        }

        body.put("api_version", apiVersion);
        body.put("idempotency_key", idempotencyKey);
        ensureSdkMetadata(body);

        effective.apiVersion = apiVersion;
        effective.idempotencyKey = idempotencyKey;
        return requestJson("POST", "/v1/jobs", body, effective);
    }

    public Map<String, Object> getJob(String jobId, RequestOptions options) {
        return requestJson("GET", "/v1/jobs/" + encode(jobId), null, options);
    }

    public Map<String, Object> listJobs(ListJobsParams params, RequestOptions options) {
        return requestJson("GET", "/v1/jobs" + buildListJobsQuery(params), null, options);
    }

    public Map<String, Object> cancelJob(String jobId, Map<String, Object> request, RequestOptions options) {
        return mutate("/v1/jobs/" + encode(jobId) + "/cancel", request, options);
    }

    public Map<String, Object> retryJob(String jobId, Map<String, Object> request, RequestOptions options) {
        return mutate("/v1/jobs/" + encode(jobId) + "/retry", request, options);
    }

    // --- Job events --------------------------------------------------------

    public Map<String, Object> listJobEvents(String jobId, ListJobEventsParams params, RequestOptions options) {
        List<String[]> pairs = new ArrayList<>();
        if (params != null) {
            addPair(pairs, "cursor", params.cursor);
            if (params.afterSequence != null && params.afterSequence > 0) {
                addPair(pairs, "after_sequence", String.valueOf(params.afterSequence));
            }
            if (params.limit != null && params.limit > 0) {
                addPair(pairs, "limit", String.valueOf(params.limit));
            }
        }
        return requestJson("GET", "/v1/jobs/" + encode(jobId) + "/events" + encodeQuery(pairs), null, options);
    }

    public String streamJobEventsSse(String jobId, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        effective.headers.put("Accept", "text/event-stream");
        Transport.Response response = send("GET", "/v1/sse/jobs/" + encode(jobId), null, null, effective);
        return new String(response.body, StandardCharsets.UTF_8);
    }

    // --- Artifacts ---------------------------------------------------------

    public Map<String, Object> listJobArtifacts(String jobId, RequestOptions options) {
        return requestJson("GET", "/v1/jobs/" + encode(jobId) + "/artifacts", null, options);
    }

    public ArtifactDownload getJobArtifact(String jobId, String key, RequestOptions options) {
        Transport.Response response =
                send("GET", "/v1/jobs/" + encode(jobId) + "/artifacts/" + encode(key), null, null, options);
        String contentType = headerValue(response.headers, "content-type");
        String checksum = headerValue(response.headers, "ubag-artifact-checksum");
        return new ArtifactDownload(response.body, contentType, checksum);
    }

    public Map<String, Object> putJobArtifact(
            String jobId, String key, byte[] body, String contentType, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        if (effective.idempotencyKey == null) {
            effective.idempotencyKey = IdempotencyKey.generate();
        }
        String resolvedType = (contentType == null || contentType.isEmpty()) ? "application/octet-stream" : contentType;
        Transport.Response response =
                send("PUT", "/v1/jobs/" + encode(jobId) + "/artifacts/" + encode(key), body, resolvedType, effective);
        return decodeJson(response.body);
    }

    public void deleteJobArtifact(String jobId, String key, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        if (effective.idempotencyKey == null) {
            effective.idempotencyKey = IdempotencyKey.generate();
        }
        requestJson("DELETE", "/v1/jobs/" + encode(jobId) + "/artifacts/" + encode(key), null, effective);
    }

    // --- Operator collections ---------------------------------------------

    public Map<String, Object> listWorkflows(RequestOptions options) {
        return requestJson("GET", "/v1/workflows", null, options);
    }

    public Map<String, Object> listTemplates(RequestOptions options) {
        return requestJson("GET", "/v1/templates", null, options);
    }

    public Map<String, Object> listTargets(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/targets" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listAdapters(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/adapters" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listApps(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/apps" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listDevices(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/devices" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listWebhooks(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/webhooks" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listAuditEvents(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/audit" + buildListQuery(params), null, options);
    }

    public Map<String, Object> listEvents(ListParams params, RequestOptions options) {
        return requestJson("GET", "/v1/events" + buildListQuery(params), null, options);
    }

    // --- Webhook replay & cache -------------------------------------------

    public Map<String, Object> replayWebhookDelivery(Map<String, Object> request, RequestOptions options) {
        return mutate("/v1/webhooks/replay", request, options);
    }

    public Map<String, Object> cacheStatus(RequestOptions options) {
        return requestJson("GET", "/v1/cache", null, options);
    }

    // --- Internal helpers --------------------------------------------------

    private Map<String, Object> mutate(String path, Map<String, Object> request, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        Map<String, Object> body = request == null ? new LinkedHashMap<>() : new LinkedHashMap<>(request);

        String apiVersion = stringField(body, "api_version");
        if (apiVersion == null) {
            apiVersion = effective.apiVersion != null ? effective.apiVersion : this.apiVersion;
        }
        String idempotencyKey = stringField(body, "idempotency_key");
        if (idempotencyKey == null) {
            idempotencyKey = effective.idempotencyKey != null ? effective.idempotencyKey : IdempotencyKey.generate();
        }

        body.put("api_version", apiVersion);
        body.put("idempotency_key", idempotencyKey);
        effective.apiVersion = apiVersion;
        effective.idempotencyKey = idempotencyKey;
        return requestJson("POST", path, body, effective);
    }

    private Map<String, Object> requestJson(
            String method, String path, Map<String, Object> body, RequestOptions options) {
        byte[] serialized = null;
        if (body != null) {
            try {
                serialized = mapper.writeValueAsBytes(body);
            } catch (Exception e) {
                throw new RuntimeException("failed to serialize request body", e);
            }
        }
        Transport.Response response = send(method, path, serialized, serialized == null ? null : JSON_CONTENT_TYPE, options);
        if (response.body == null || response.body.length == 0 || response.status == 204) {
            return new LinkedHashMap<>();
        }
        return decodeJson(response.body);
    }

    private Transport.Response send(
            String method, String path, byte[] body, String contentType, RequestOptions options) {
        RequestOptions effective = options == null ? RequestOptions.create() : options;
        String url = baseUrl + path;
        String resolvedApiVersion = effective.apiVersion != null ? effective.apiVersion : this.apiVersion;

        Map<String, String> headers = new LinkedHashMap<>();
        headers.put("Accept", JSON_CONTENT_TYPE);
        headers.put("Ubag-Api-Version", resolvedApiVersion);
        headers.put("Ubag-Sdk-Name", SDK_NAME);
        headers.put("Ubag-Sdk-Version", SDK_VERSION);
        headers.putAll(defaultHeaders);
        headers.putAll(effective.headers);
        if (appSecret != null && !headers.containsKey("Authorization")) {
            headers.put("Authorization", "Bearer " + appSecret);
        }
        if (effective.idempotencyKey != null) {
            headers.put("Idempotency-Key", effective.idempotencyKey);
        }
        if (body != null) {
            headers.put("Content-Type", contentType != null ? contentType : JSON_CONTENT_TYPE);
        }

        Transport.Request request = new Transport.Request(method, url, headers, body);
        Transport.Response response;
        try {
            response = transport.execute(request);
        } catch (Exception e) {
            throw new UbagTransportException(method, url, e);
        }
        if (response.status < 200 || response.status >= 300) {
            Map<String, Object> envelope = parseEnvelope(response.body);
            throw new UbagApiException(response.status, method, url, response.headers, response.body, envelope);
        }
        return response;
    }

    private Map<String, Object> parseEnvelope(byte[] body) {
        if (body == null || body.length == 0) {
            return null;
        }
        try {
            Map<String, Object> parsed = decodeJson(body);
            Object error = parsed.get("error");
            if (error instanceof Map<?, ?> details) {
                Object code = details.get("code");
                if (code instanceof String text && text.startsWith("UBAG-")) {
                    return parsed;
                }
            }
        } catch (RuntimeException ignored) {
            // Not a JSON envelope; leave it null so raw body is preserved.
        }
        return null;
    }

    @SuppressWarnings("unchecked")
    private Map<String, Object> decodeJson(byte[] body) {
        try {
            return mapper.readValue(body, Map.class);
        } catch (Exception e) {
            throw new RuntimeException("failed to parse response body", e);
        }
    }

    @SuppressWarnings("unchecked")
    private static void ensureSdkMetadata(Map<String, Object> body) {
        Object existing = body.get("client");
        Map<String, Object> clientMetadata;
        if (existing instanceof Map<?, ?> map) {
            clientMetadata = (Map<String, Object>) map;
        } else {
            clientMetadata = new LinkedHashMap<>();
            body.put("client", clientMetadata);
        }
        if (!clientMetadata.containsKey("sdk")) {
            Map<String, Object> sdk = new LinkedHashMap<>();
            sdk.put("name", SDK_NAME);
            sdk.put("version", SDK_VERSION);
            clientMetadata.put("sdk", sdk);
        }
    }

    private static String stringField(Map<String, Object> body, String key) {
        Object value = body.get(key);
        if (value instanceof String text && !text.isEmpty()) {
            return text;
        }
        return null;
    }

    private static String headerValue(Map<String, String> headers, String key) {
        for (Map.Entry<String, String> entry : headers.entrySet()) {
            if (entry.getKey().equalsIgnoreCase(key)) {
                return entry.getValue();
            }
        }
        return "";
    }

    private static void addPair(List<String[]> pairs, String key, String value) {
        if (value != null && !value.isEmpty()) {
            pairs.add(new String[] {key, value});
        }
    }

    private static String buildListQuery(ListParams params) {
        List<String[]> pairs = new ArrayList<>();
        if (params != null) {
            addPair(pairs, "cursor", params.cursor);
            if (params.limit != null && params.limit > 0) {
                addPair(pairs, "limit", String.valueOf(params.limit));
            }
        }
        return encodeQuery(pairs);
    }

    private static String buildListJobsQuery(ListJobsParams params) {
        List<String[]> pairs = new ArrayList<>();
        if (params != null) {
            addPair(pairs, "cursor", params.cursor);
            if (params.limit != null && params.limit > 0) {
                addPair(pairs, "limit", String.valueOf(params.limit));
            }
            addPair(pairs, "filter[status]", params.status);
            addPair(pairs, "filter[target]", params.target);
            addPair(pairs, "sort", params.sort);
            if (params.fields != null && !params.fields.isEmpty()) {
                addPair(pairs, "fields", String.join(",", params.fields));
            }
            if (params.include != null && !params.include.isEmpty()) {
                addPair(pairs, "include", String.join(",", params.include));
            }
        }
        return encodeQuery(pairs);
    }

    private static String encodeQuery(List<String[]> pairs) {
        if (pairs.isEmpty()) {
            return "";
        }
        StringBuilder builder = new StringBuilder("?");
        for (int i = 0; i < pairs.size(); i++) {
            if (i > 0) {
                builder.append('&');
            }
            builder.append(encode(pairs.get(i)[0])).append('=').append(encode(pairs.get(i)[1]));
        }
        return builder.toString();
    }

    private static String encode(String value) {
        return URLEncoder.encode(value, StandardCharsets.UTF_8).replace("+", "%20");
    }

    /** A downloaded artifact: raw bytes plus content metadata. */
    public static final class ArtifactDownload {
        public final byte[] body;
        public final String contentType;
        public final String checksum;

        public ArtifactDownload(byte[] body, String contentType, String checksum) {
            this.body = body;
            this.contentType = contentType;
            this.checksum = checksum;
        }
    }

    /** Cursor pagination parameters. */
    public static final class ListParams {
        public String cursor;
        public Integer limit;
    }

    /** Filtering and pagination parameters for {@link #listJobs}. */
    public static final class ListJobsParams {
        public String cursor;
        public Integer limit;
        public String status;
        public String target;
        public String sort;
        public List<String> fields;
        public List<String> include;
    }

    /** Pagination parameters for per-job event listing. */
    public static final class ListJobEventsParams {
        public String cursor;
        public Integer afterSequence;
        public Integer limit;
    }
}
