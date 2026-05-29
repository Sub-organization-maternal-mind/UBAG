package com.ubag.sdk;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;

/** Default {@link Transport} backed by {@link java.net.http.HttpClient}. */
public final class JdkHttpTransport implements Transport {

    private final HttpClient client;

    public JdkHttpTransport() {
        this(HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(30)).build());
    }

    public JdkHttpTransport(HttpClient client) {
        this.client = client;
    }

    @Override
    public Response execute(Request request) throws Exception {
        HttpRequest.Builder builder = HttpRequest.newBuilder().uri(URI.create(request.url));

        HttpRequest.BodyPublisher publisher = request.body == null
                ? HttpRequest.BodyPublishers.noBody()
                : HttpRequest.BodyPublishers.ofByteArray(request.body);
        builder.method(request.method, publisher);

        for (Map.Entry<String, String> header : request.headers.entrySet()) {
            builder.header(header.getKey(), header.getValue());
        }

        HttpResponse<byte[]> response = client.send(builder.build(), HttpResponse.BodyHandlers.ofByteArray());

        Map<String, String> headers = new LinkedHashMap<>();
        response.headers().map().forEach((name, values) -> {
            if (!values.isEmpty()) {
                headers.put(name.toLowerCase(), values.get(0));
            }
        });
        return new Response(response.statusCode(), headers, response.body());
    }
}
