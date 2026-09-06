import { test } from "node:test";
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { verifyWebhookSignature } from "../dist/webhooks.js";

const SECRET = "whsec_test";

// Mirror of the gateway's signing (internal/webhooks signing.go):
// HMAC-SHA256 over `${timestamp}.${nonce}.${body}`, base64url, "v1=" prefix.
function sign(timestamp, nonce, body) {
  const base = `${timestamp}.${nonce}.${body}`;
  const digest = createHmac("sha256", SECRET).update(base).digest("base64url");
  return `v1=${digest}`;
}

test("verifyWebhookSignature accepts a valid recent gateway signature", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  const body = '{"event":"job.completed"}';
  const sig = sign(ts, "nonce_01", body);
  assert.equal(
    await verifyWebhookSignature(Buffer.from(body), sig, SECRET, { timestamp: ts, nonce: "nonce_01" }),
    true,
  );
});

test("verifyWebhookSignature rejects a wrong nonce (replayed signature)", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  const body = '{"event":"job.completed"}';
  const sig = sign(ts, "nonce_01", body);
  assert.equal(
    await verifyWebhookSignature(Buffer.from(body), sig, SECRET, { timestamp: ts, nonce: "nonce_02" }),
    false,
  );
});

test("verifyWebhookSignature rejects a wrong signature", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  assert.equal(
    await verifyWebhookSignature(Buffer.from("x"), "v1=deadbeef", SECRET, { timestamp: ts, nonce: "n" }),
    false,
  );
});

test("verifyWebhookSignature rejects the version-less hex format", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  const body = "x";
  // Old SDK bug: hex over `${timestamp}.${body}` — must no longer verify.
  const legacy = createHmac("sha256", SECRET).update(`${ts}.${body}`).digest("hex");
  assert.equal(
    await verifyWebhookSignature(Buffer.from(body), legacy, SECRET, { timestamp: ts, nonce: "n" }),
    false,
  );
});

test("verifyWebhookSignature rejects an expired timestamp", async () => {
  const ts = (Math.floor(Date.now() / 1000) - 10 * 60).toString(); // 10 min old
  const body = "x";
  const sig = sign(ts, "nonce_01", body);
  assert.equal(
    await verifyWebhookSignature(Buffer.from(body), sig, SECRET, { timestamp: ts, nonce: "nonce_01" }),
    false,
  );
});
