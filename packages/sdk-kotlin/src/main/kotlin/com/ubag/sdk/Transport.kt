package com.ubag.sdk

/** A single HTTP request to be sent by a [Transport]. */
data class TransportRequest(
    val method: String,
    val url: String,
    val headers: Map<String, String>,
    val body: ByteArray?,
    val contentType: String?,
) {
    override fun equals(other: Any?): Boolean = this === other
    override fun hashCode(): Int = System.identityHashCode(this)
}

/** A raw HTTP response captured by a [Transport]. */
data class TransportResponse(
    val status: Int,
    val headers: Map<String, String>,
    val body: ByteArray,
) {
    override fun equals(other: Any?): Boolean = this === other
    override fun hashCode(): Int = System.identityHashCode(this)
}

/**
 * Pluggable HTTP transport. The default implementation uses OkHttp; tests
 * provide a capturing implementation to assert request construction.
 */
fun interface Transport {
    fun send(request: TransportRequest): TransportResponse
}
