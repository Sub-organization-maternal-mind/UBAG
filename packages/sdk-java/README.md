# UBAG Java SDK (`com.ubag:ubag-sdk`)

Idiomatic Java client for the UBAG v0 REST gateway, built on
`java.net.http.HttpClient`, with stable error envelopes, automatic idempotency
keys, and a pluggable {@link com.ubag.sdk.Transport} for testing.

## Endpoint coverage

- **System:** `health`, `ready`, `version`, `metrics`
- **Jobs:** `createJob`, `getJob`, `listJobs`, `cancelJob`, `retryJob`
- **Job events:** `listJobEvents`, `streamJobEventsSse`
- **Artifacts:** `listJobArtifacts`, `getJobArtifact`, `putJobArtifact`,
  `deleteJobArtifact`
- **Operator collections:** `listWorkflows`, `listTemplates`, `listTargets`,
  `listAdapters`, `listApps`, `listDevices`, `listWebhooks`, `listAuditEvents`,
  `listEvents`
- **Webhook replay & cache:** `replayWebhookDelivery`, `cacheStatus`

## Invariants

- API version `2026-05-22` sent on every request as `Ubag-Api-Version`.
- `Authorization: Bearer <app-secret>` attached when an app secret is set.
- Mutating calls send an `Idempotency-Key`, auto-generating a 26-character
  ULID-style key when one is not supplied.
- Non-2xx responses parse the stable `{ "error": { code, message, ... } }`
  envelope into a `UbagApiException`.
- Secrets are never logged or stored beyond the in-memory client.

## Example

```java
UbagClient client = UbagClient.builder("http://127.0.0.1:7878")
        .appSecret("dev-secret")
        .build();

Map<String, Object> request = new LinkedHashMap<>();
request.put("client", Map.of("app_id", "demo", "app_version", "0.0.0"));
request.put("job", Map.of("target", "mock_target", "command_type", "echo",
        "input", Map.of("prompt", "Hello UBAG")));

Map<String, Object> job = client.createJob(request, UbagClient.RequestOptions.create());
System.out.println(job.get("job_id"));
```

## Tests

```powershell
cd packages/sdk-java
mvn test
```

The test injects a capturing `Transport`, so no network is contacted at run
time.

## Toolchain

- **Required:** JDK 17+ and Maven (`mvn`).
- **Offline:** Maven must download `jackson-databind` and `junit-jupiter` on the
  first build. After the local Maven repository is populated, `mvn test` runs
  offline (`mvn -o test`).
