package com.ubag.sdk

import okhttp3.Headers.Companion.toHeaders
import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Default [Transport] backed by OkHttp. */
class OkHttpTransport(private val client: OkHttpClient = OkHttpClient()) : Transport {
    override fun send(request: TransportRequest): TransportResponse {
        val builder = Request.Builder().url(request.url)

        val headers = request.headers.filterKeys { !it.equals("Content-Type", ignoreCase = true) }
        builder.headers(headers.toHeaders())

        val requestBody = request.body?.let { bytes ->
            val mediaType = (request.contentType ?: "application/octet-stream").toMediaTypeOrNull()
            bytes.toRequestBody(mediaType)
        }
        builder.method(request.method, requestBody)

        client.newCall(builder.build()).execute().use { response ->
            val responseHeaders = mutableMapOf<String, String>()
            for (name in response.headers.names()) {
                responseHeaders[name.lowercase()] = response.headers[name] ?: ""
            }
            val body = response.body?.bytes() ?: ByteArray(0)
            return TransportResponse(response.code, responseHeaders, body)
        }
    }
}
