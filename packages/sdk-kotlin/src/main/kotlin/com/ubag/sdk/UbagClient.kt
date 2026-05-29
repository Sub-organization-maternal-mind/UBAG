package com.ubag.sdk

import org.json.JSONObject
import java.net.URLEncoder
import java.nio.charset.StandardCharsets

/** Per-call overrides for a request. */
data class RequestOptions(
    var idempotencyKey: String? = null,
    var apiVersion: String? = null,
    val headers: MutableMap<String, String> = mutableMapOf(),
)

/** Cursor pagination parameters shared by operator list endpoints. */
open class ListParams(
    var cursor: String? = null,
    var limit: Int? = null,
)

/** Filter and projection parameters for `GET /v1/jobs`. */
class ListJobsParams(
    cursor: String? = null,
    limit: Int? = null,
    var status: String? = null,
    var target: String? = null,
    var sort: String? = null,
    var fields: List<String>? = null,
    var include: List<String>? = null,
) : ListParams(cursor, limit)

/** Pagination parameters for `GET /v1/jobs/{id}/events`. */
data class ListJobEventsParams(
    var cursor: String? = null,
    var afterSequence: Long? = null,
    var limit: Int? = null,
)

/** A downloaded artifact: raw bytes plus content type and checksum. */
data class ArtifactDownload(
    val body: ByteArray,
    val contentType: String,
    val checksum: String,
) {
    override fun equals(other: Any?): Boolean = this === other
    override fun hashCode(): Int = System.identityHashCode(this)
}

/** Kotlin client for the UBAG v0 REST gateway. */
class UbagClient(
    baseUrl: String,
    private val appSecret: String? = null,
    private val apiVersion: String = API_VERSION,
    private val transport: Transport = OkHttpTransport(),
    private val defaultHeaders: Map<String, String> = emptyMap(),
) {
    private val baseUrl: String

    init {
        val trimmed = baseUrl.trim()
        require(trimmed.isNotEmpty()) { "baseUrl is required" }
        require(trimmed.contains("://")) { "baseUrl must include scheme and host" }
        this.baseUrl = trimmed.trimEnd('/')
    }

    // --- System -----------------------------------------------------------

    fun health(options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/health", null, options)

    fun ready(options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/ready", null, options)

    fun version(options: RequestOptions? = null): JSONObject {
        options?.idempotencyKey = null
        return requestJson("GET", "/v1/version", null, options)
    }

    fun metrics(options: RequestOptions? = null): String {
        val resolved = withHeader(options, "Accept", "text/plain")
        return String(send("GET", "/v1/metrics", null, null, resolved).body, StandardCharsets.UTF_8)
    }

    // --- Jobs --------------------------------------------------------------

    fun createJob(request: Map<String, Any?>, options: RequestOptions? = null): JSONObject {
        val body = JSONObject(request)
        val resolvedVersion = stringField(body, "api_version") ?: options?.apiVersion ?: apiVersion
        val key = stringField(body, "idempotency_key") ?: options?.idempotencyKey ?: IdempotencyKey.generate()

        body.put("api_version", resolvedVersion)
        body.put("idempotency_key", key)
        ensureSdkMetadata(body)

        return requestJson("POST", "/v1/jobs", body, withIdempotency(options, resolvedVersion, key))
    }

    fun getJob(jobId: String, options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/jobs/${encode(jobId)}", null, options)

    fun listJobs(params: ListJobsParams = ListJobsParams(), options: RequestOptions? = null): JSONObject {
        val pairs = mutableListOf<Pair<String, String>>()
        addPair(pairs, "cursor", params.cursor)
        addPair(pairs, "limit", params.limit)
        addPair(pairs, "filter[status]", params.status)
        addPair(pairs, "filter[target]", params.target)
        addPair(pairs, "sort", params.sort)
        addPair(pairs, "fields", params.fields?.joinToString(","))
        addPair(pairs, "include", params.include?.joinToString(","))
        return requestJson("GET", "/v1/jobs" + encodeQuery(pairs), null, options)
    }

    fun cancelJob(jobId: String, request: Map<String, Any?> = emptyMap(), options: RequestOptions? = null): JSONObject =
        mutate("/v1/jobs/${encode(jobId)}/cancel", request, options)

    fun retryJob(jobId: String, request: Map<String, Any?> = emptyMap(), options: RequestOptions? = null): JSONObject =
        mutate("/v1/jobs/${encode(jobId)}/retry", request, options)

    // --- Job events --------------------------------------------------------

    fun listJobEvents(jobId: String, params: ListJobEventsParams = ListJobEventsParams(), options: RequestOptions? = null): JSONObject {
        val pairs = mutableListOf<Pair<String, String>>()
        addPair(pairs, "cursor", params.cursor)
        addPair(pairs, "after_sequence", params.afterSequence)
        addPair(pairs, "limit", params.limit)
        return requestJson("GET", "/v1/jobs/${encode(jobId)}/events" + encodeQuery(pairs), null, options)
    }

    fun streamJobEventsSse(jobId: String, options: RequestOptions? = null): String {
        val resolved = withHeader(options, "Accept", "text/event-stream")
        return String(send("GET", "/v1/sse/jobs/${encode(jobId)}", null, null, resolved).body, StandardCharsets.UTF_8)
    }

    // --- Artifacts ---------------------------------------------------------

    fun listJobArtifacts(jobId: String, options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/jobs/${encode(jobId)}/artifacts", null, options)

    fun getJobArtifact(jobId: String, key: String, options: RequestOptions? = null): ArtifactDownload {
        val response = send("GET", "/v1/jobs/${encode(jobId)}/artifacts/${encode(key)}", null, null, options)
        return ArtifactDownload(
            response.body,
            response.headers["content-type"] ?: "",
            response.headers["ubag-artifact-checksum"] ?: "",
        )
    }

    fun putJobArtifact(
        jobId: String,
        key: String,
        body: ByteArray,
        contentType: String = "application/octet-stream",
        options: RequestOptions? = null,
    ): JSONObject {
        val resolved = withIdempotency(options, options?.apiVersion ?: apiVersion, options?.idempotencyKey ?: IdempotencyKey.generate())
        val resolvedType = contentType.ifEmpty { "application/octet-stream" }
        val response = send("PUT", "/v1/jobs/${encode(jobId)}/artifacts/${encode(key)}", body, resolvedType, resolved)
        return decodeJson(response.body)
    }

    fun deleteJobArtifact(jobId: String, key: String, options: RequestOptions? = null) {
        val resolved = withIdempotency(options, options?.apiVersion ?: apiVersion, options?.idempotencyKey ?: IdempotencyKey.generate())
        send("DELETE", "/v1/jobs/${encode(jobId)}/artifacts/${encode(key)}", null, null, resolved)
    }

    // --- Operator collections ---------------------------------------------

    fun listWorkflows(options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/workflows", null, options)

    fun listTemplates(options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/templates", null, options)

    fun listTargets(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/targets" + buildListQuery(params), null, options)

    fun listAdapters(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/adapters" + buildListQuery(params), null, options)

    fun listApps(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/apps" + buildListQuery(params), null, options)

    fun listDevices(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/devices" + buildListQuery(params), null, options)

    fun listWebhooks(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/webhooks" + buildListQuery(params), null, options)

    fun listAuditEvents(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/audit" + buildListQuery(params), null, options)

    fun listEvents(params: ListParams = ListParams(), options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/events" + buildListQuery(params), null, options)

    fun replayWebhookDelivery(request: Map<String, Any?> = emptyMap(), options: RequestOptions? = null): JSONObject =
        mutate("/v1/webhooks/replay", request, options)

    fun cacheStatus(options: RequestOptions? = null): JSONObject =
        requestJson("GET", "/v1/cache", null, options)

    // --- Internal helpers --------------------------------------------------

    private fun mutate(path: String, request: Map<String, Any?>, options: RequestOptions?): JSONObject {
        val body = JSONObject(request)
        val resolvedVersion = stringField(body, "api_version") ?: options?.apiVersion ?: apiVersion
        val key = stringField(body, "idempotency_key") ?: options?.idempotencyKey ?: IdempotencyKey.generate()

        body.put("api_version", resolvedVersion)
        body.put("idempotency_key", key)
        return requestJson("POST", path, body, withIdempotency(options, resolvedVersion, key))
    }

    private fun requestJson(method: String, path: String, body: JSONObject?, options: RequestOptions?): JSONObject {
        val serialized = body?.toString()?.toByteArray(StandardCharsets.UTF_8)
        val contentType = if (serialized == null) null else JSON_CONTENT_TYPE
        val response = send(method, path, serialized, contentType, options)
        if (response.body.isEmpty() || response.status == 204) {
            return JSONObject()
        }
        return decodeJson(response.body)
    }

    private fun send(method: String, path: String, body: ByteArray?, contentType: String?, options: RequestOptions?): TransportResponse {
        val url = baseUrl + path
        val resolvedVersion = options?.apiVersion ?: apiVersion

        val headers = linkedMapOf(
            "Accept" to JSON_CONTENT_TYPE,
            "Ubag-Api-Version" to resolvedVersion,
            "Ubag-Sdk-Name" to SDK_NAME,
            "Ubag-Sdk-Version" to SDK_VERSION,
        )
        headers.putAll(defaultHeaders)
        options?.headers?.let { headers.putAll(it) }
        if (appSecret != null && !headers.containsKey("Authorization")) {
            headers["Authorization"] = "Bearer $appSecret"
        }
        options?.idempotencyKey?.takeIf { it.isNotEmpty() }?.let { headers["Idempotency-Key"] = it }
        if (body != null) {
            headers["Content-Type"] = contentType ?: JSON_CONTENT_TYPE
        }

        val request = TransportRequest(method, url, headers, body, if (body == null) null else contentType ?: JSON_CONTENT_TYPE)

        val response = try {
            transport.send(request)
        } catch (cause: Throwable) {
            throw UbagTransportException(method, url, cause)
        }

        if (response.status < 200 || response.status >= 300) {
            val rawBody = String(response.body, StandardCharsets.UTF_8)
            throw UbagApiException(response.status, method, url, response.headers, rawBody, parseEnvelope(rawBody))
        }

        return response
    }

    private fun parseEnvelope(rawBody: String): JSONObject? {
        if (rawBody.isEmpty()) return null
        return try {
            val parsed = JSONObject(rawBody)
            val code = parsed.optJSONObject("error")?.optString("code", "") ?: ""
            if (code.startsWith("UBAG-")) parsed else null
        } catch (_: Exception) {
            null
        }
    }

    private fun decodeJson(body: ByteArray): JSONObject {
        val text = String(body, StandardCharsets.UTF_8)
        return if (text.isEmpty()) JSONObject() else JSONObject(text)
    }

    private fun ensureSdkMetadata(body: JSONObject) {
        val client = body.optJSONObject("client")
        if (client != null) {
            if (!client.has("sdk")) {
                client.put("sdk", JSONObject().put("name", SDK_NAME).put("version", SDK_VERSION))
            }
        } else {
            body.put("client", JSONObject().put("sdk", JSONObject().put("name", SDK_NAME).put("version", SDK_VERSION)))
        }
    }

    private fun stringField(body: JSONObject, key: String): String? {
        if (!body.has(key)) return null
        val value = body.optString(key, "")
        return value.ifEmpty { null }
    }

    private fun withHeader(options: RequestOptions?, name: String, value: String): RequestOptions {
        val resolved = options ?: RequestOptions()
        resolved.headers[name] = value
        return resolved
    }

    private fun withIdempotency(options: RequestOptions?, apiVersion: String, key: String): RequestOptions {
        val resolved = options ?: RequestOptions()
        resolved.apiVersion = apiVersion
        resolved.idempotencyKey = key
        return resolved
    }

    private fun addPair(pairs: MutableList<Pair<String, String>>, key: String, value: String?) {
        if (!value.isNullOrEmpty()) pairs.add(key to value)
    }

    private fun addPair(pairs: MutableList<Pair<String, String>>, key: String, value: Int?) {
        if (value != null && value > 0) pairs.add(key to value.toString())
    }

    private fun addPair(pairs: MutableList<Pair<String, String>>, key: String, value: Long?) {
        if (value != null && value > 0) pairs.add(key to value.toString())
    }

    private fun buildListQuery(params: ListParams): String {
        val pairs = mutableListOf<Pair<String, String>>()
        addPair(pairs, "cursor", params.cursor)
        addPair(pairs, "limit", params.limit)
        return encodeQuery(pairs)
    }

    private fun encodeQuery(pairs: List<Pair<String, String>>): String {
        if (pairs.isEmpty()) return ""
        return "?" + pairs.joinToString("&") { "${encode(it.first)}=${encode(it.second)}" }
    }

    private fun encode(value: String): String = URLEncoder.encode(value, StandardCharsets.UTF_8)

    companion object {
        const val API_VERSION = "2026-05-22"
        const val SDK_NAME = "ubag-kotlin"
        const val SDK_VERSION = "0.0.0"
        private const val JSON_CONTENT_TYPE = "application/json"
    }
}
