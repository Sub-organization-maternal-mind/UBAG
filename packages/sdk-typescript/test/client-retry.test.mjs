import { test } from "node:test";
import assert from "node:assert/strict";
import { UbagClient } from "../dist/client.js";

test("client retries a retryable 503 then succeeds", async () => {
  let calls = 0;
  const fetch = async (url, init) => {
    calls++;
    if (calls === 1) {
      return new Response(
        JSON.stringify({ error: { code: "UBAG-QUEUE-ENQUEUE-001", category: "queue", message: "busy", retryable: true, trace_id: "t1" } }),
        { status: 503, headers: { "content-type": "application/json" } },
      );
    }
    return new Response(
      JSON.stringify({ api_version: "2026-05-22", job_id: "job_1", status: "queued", target: "mock", result: null, metadata: {}, trace_id: "t2", events_url: "", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" }),
      { status: 202, headers: { "content-type": "application/json" } },
    );
  };
  const client = new UbagClient({
    baseUrl: "https://gw.example",
    fetch,
    sidecarDiscovery: false,
    retry: { maxAttempts: 3, baseDelayMs: 1, maxDelayMs: 5 },
  });
  const job = await client.createJob({ client: { app_id: "a", app_version: "1", sdk: { name: "s", version: "1" } }, job: { target: "mock", command_type: "submit", input: {} } }, { idempotencyKey: "key_0123456789abcdef" });
  assert.equal(job.job_id, "job_1");
  assert.equal(calls, 2);
});
