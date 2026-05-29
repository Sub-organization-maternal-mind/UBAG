package com.ubag.sdk;

import java.util.Map;

/** Thrown when the gateway returns a non-2xx response. */
public class UbagApiException extends RuntimeException {

    private final int status;
    private final String method;
    private final String url;
    private final Map<String, String> headers;
    private final byte[] rawBody;
    private final Map<String, Object> envelope;

    public UbagApiException(
            int status,
            String method,
            String url,
            Map<String, String> headers,
            byte[] rawBody,
            Map<String, Object> envelope) {
        super(messageFor(status, envelope));
        this.status = status;
        this.method = method;
        this.url = url;
        this.headers = headers;
        this.rawBody = rawBody;
        this.envelope = envelope;
    }

    private static String messageFor(int status, Map<String, Object> envelope) {
        if (envelope != null) {
            Object error = envelope.get("error");
            if (error instanceof Map<?, ?> details) {
                Object message = details.get("message");
                if (message instanceof String text && !text.isEmpty()) {
                    return text;
                }
            }
        }
        return "UBAG API request failed with HTTP " + status;
    }

    public int status() {
        return status;
    }

    public String method() {
        return method;
    }

    public String url() {
        return url;
    }

    public Map<String, String> headers() {
        return headers;
    }

    public byte[] rawBody() {
        return rawBody;
    }

    public Map<String, Object> envelope() {
        return envelope;
    }

    @SuppressWarnings("unchecked")
    private Object errorField(String key) {
        if (envelope == null) {
            return null;
        }
        Object error = envelope.get("error");
        if (error instanceof Map<?, ?> details) {
            return ((Map<String, Object>) details).get(key);
        }
        return null;
    }

    public String code() {
        Object value = errorField("code");
        return value instanceof String text ? text : null;
    }

    public String category() {
        Object value = errorField("category");
        return value instanceof String text ? text : null;
    }

    public boolean retryable() {
        return Boolean.TRUE.equals(errorField("retryable"));
    }

    public String traceId() {
        Object value = errorField("trace_id");
        if (value instanceof String text && !text.isEmpty()) {
            return text;
        }
        String header = headers.get("ubag-trace-id");
        return header != null ? header : headers.get("x-request-id");
    }
}
