import { test } from "node:test";
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { verifyWebhookSignature } from "../dist/webhooks.js";

const SECRET = "whsec_test";

function sign(timestamp, body) {
  const base = `${timestamp}.${body}`;
  return createHmac("sha256", SECRET).update(base).digest("hex");
}

test("verifyWebhookSignature accepts a valid recent signature", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  const body = '{"event":"job.completed"}';
  const sig = sign(ts, body);
  assert.equal(await verifyWebhookSignature(Buffer.from(body), sig, SECRET, { timestamp: ts }), true);
});

test("verifyWebhookSignature rejects a wrong signature", async () => {
  const ts = Math.floor(Date.now() / 1000).toString();
  assert.equal(await verifyWebhookSignature(Buffer.from("x"), "deadbeef", SECRET, { timestamp: ts }), false);
});

test("verifyWebhookSignature rejects an expired timestamp", async () => {
  const ts = (Math.floor(Date.now() / 1000) - 10 * 60).toString(); // 10 min old
  const body = "x";
  const sig = sign(ts, body);
  assert.equal(await verifyWebhookSignature(Buffer.from(body), sig, SECRET, { timestamp: ts }), false);
});
