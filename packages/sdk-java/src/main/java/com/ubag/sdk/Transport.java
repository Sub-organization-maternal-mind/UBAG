package com.ubag.sdk;

import java.util.Map;

/**
 * Pluggable HTTP transport. The default implementation uses
 * {@link java.net.http.HttpClient}; tests provide a capturing implementation to
 * assert request construction without a live gateway.
 */
public interface Transport {

    Response execute(Request request) throws Exception;

    /** A fully constructed HTTP request. */
    final class Request {
        public final String method;
        public final String url;
        public final Map<String, String> headers;
        public final byte[] body;

        public Request(String method, String url, Map<String, String> headers, byte[] body) {
            this.method = method;
            this.url = url;
            this.headers = headers;
            this.body = body;
        }
    }

    /** A raw HTTP response. */
    final class Response {
        public final int status;
        public final Map<String, String> headers;
        public final byte[] body;

        public Response(int status, Map<String, String> headers, byte[] body) {
            this.status = status;
            this.headers = headers;
            this.body = body;
        }
    }
}
