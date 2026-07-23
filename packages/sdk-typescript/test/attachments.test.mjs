import { test } from "node:test";
import assert from "node:assert/strict";
import {
  UBAG_ATTACHMENT_MAX_FILE_BYTES,
  UBAG_ATTACHMENT_MAX_MANIFEST_FILES,
  UbagClient,
} from "../dist/index.js";

const request = {
  client: {},
  job: { target: "mock", command_type: "chat.prompt", input: { prompt: "inspect" } },
};

const response = (body) =>
  new Response(JSON.stringify(body), {
    status: 202,
    headers: { "content-type": "application/json" },
  });

test("createJobWithAttachments uploads every key-reference body", async () => {
  const seen = [];
  const client = new UbagClient({
    baseUrl: "https://gateway.example",
    fetch: async (url, init) => {
      seen.push({ url: String(url), init });
      return String(url).endsWith("/v1/jobs")
        ? response({ job_id: "job_1" })
        : response({ key: String(url).split("/").at(-1) });
    },
  });
  const attachments = [
    { key: "a.txt", content_type: "text/plain", kind: "document", body: "alpha" },
    { key: "b.wav", content_type: "audio/wav", kind: "voice", body: new Uint8Array([1, 2]) },
  ];

  await client.createJobWithAttachments(request, attachments);

  assert.equal(seen.length, 3);
  assert.deepEqual(
    seen.slice(1).map(({ url }) => url.split("/").at(-1)).sort(),
    ["a.txt", "b.wav"],
  );
});

test("multipart sends job first, preserves metadata and request headers", async () => {
  let captured;
  const client = new UbagClient({
    baseUrl: "https://gateway.example",
    appSecret: "test-secret",
    fetch: async (_url, init) => {
      captured = init;
      return response({ job_id: "job_2" });
    },
  });

  await client.createJobMultipart(
    request,
    [
      {
        key: "report.pdf",
        filename: "clinical-report.pdf",
        content_type: "application/pdf",
        kind: "document",
        body: new Uint8Array([37, 80, 68, 70]),
      },
      {
        key: "voice.webm",
        filename: "note.webm",
        content_type: "audio/webm",
        kind: "voice",
        body: new Uint8Array([26, 69, 223, 163]),
      },
    ],
    { idempotencyKey: "idem-attachments" },
  );

  assert.ok(captured.body instanceof FormData);
  assert.deepEqual([...captured.body.keys()], ["job", "report.pdf", "voice.webm"]);
  assert.equal(captured.body.get("report.pdf").type, "application/pdf");
  assert.equal(captured.body.get("report.pdf").name, "clinical-report.pdf");
  assert.equal(captured.body.get("voice.webm").type, "audio/webm");
  assert.equal(captured.headers.get("Authorization"), "Bearer test-secret");
  assert.equal(captured.headers.get("Ubag-Api-Version"), "2026-05-22");
  assert.equal(captured.headers.get("Idempotency-Key"), "idem-attachments");
  assert.equal(captured.headers.has("Content-Type"), false);
});

test("exports attachment hard limits", () => {
  assert.equal(UBAG_ATTACHMENT_MAX_FILE_BYTES, 32 * 1024 * 1024);
  assert.equal(UBAG_ATTACHMENT_MAX_MANIFEST_FILES, 32);
});
