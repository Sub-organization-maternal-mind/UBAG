package com.ubag.sdk

import org.json.JSONObject

/** Thrown when the gateway returns a non-2xx response. */
class UbagApiException(
    val status: Int,
    val httpMethod: String,
    val url: String,
    val headers: Map<String, String>,
    val rawBody: String,
    val envelope: JSONObject?,
) : RuntimeException(messageFor(status, envelope)) {

    fun code(): String? = errorField("code")

    fun category(): String? = errorField("category")

    fun retryable(): Boolean = envelope?.optJSONObject("error")?.optBoolean("retryable", false) ?: false

    fun traceId(): String? {
        errorField("trace_id")?.let { return it }
        headers["ubag-trace-id"]?.takeIf { it.isNotEmpty() }?.let { return it }
        headers["x-request-id"]?.takeIf { it.isNotEmpty() }?.let { return it }
        return null
    }

    private fun errorField(name: String): String? {
        val value = envelope?.optJSONObject("error")?.optString(name, "")
        return if (value.isNullOrEmpty()) null else value
    }

    companion object {
        private fun messageFor(status: Int, envelope: JSONObject?): String {
            val message = envelope?.optJSONObject("error")?.optString("message", "")
            return if (!message.isNullOrEmpty()) message else "UBAG API request failed with HTTP $status"
        }
    }
}

/** Thrown when a request could not be sent (network/transport failure). */
class UbagTransportException(
    val httpMethod: String,
    val url: String,
    cause: Throwable,
) : RuntimeException("UBAG API request could not be sent: $httpMethod $url: ${cause.message}", cause)
