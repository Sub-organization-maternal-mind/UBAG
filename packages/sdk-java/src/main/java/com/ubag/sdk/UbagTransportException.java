package com.ubag.sdk;

/** Thrown when a request could not be sent (network/transport failure). */
public class UbagTransportException extends RuntimeException {

    private final String method;
    private final String url;

    public UbagTransportException(String method, String url, Throwable cause) {
        super("UBAG API request could not be sent: " + method + " " + url, cause);
        this.method = method;
        this.url = url;
    }

    public String method() {
        return method;
    }

    public String url() {
        return url;
    }
}
