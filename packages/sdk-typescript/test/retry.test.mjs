import { test } from "node:test";
import assert from "node:assert/strict";
import { computeBackoff, DEFAULT_RETRY_POLICY } from "../dist/retry.js";

test("computeBackoff grows exponentially within jitter bounds", () => {
  const policy = { maxAttempts: 5, baseDelayMs: 1000, maxDelayMs: 60000 };
  const d0 = computeBackoff(policy, 0, () => 0.5); // midpoint jitter
  const d1 = computeBackoff(policy, 1, () => 0.5);
  assert.ok(d1 > d0, "delay should increase with attempt");
});

test("computeBackoff caps at maxDelayMs", () => {
  const policy = { maxAttempts: 10, baseDelayMs: 1000, maxDelayMs: 2000 };
  const d = computeBackoff(policy, 9, () => 1.0); // max jitter
  assert.ok(d <= 2000 * 1.3, "delay should not exceed cap + jitter");
});

test("DEFAULT_RETRY_POLICY has 3 attempts", () => {
  assert.equal(DEFAULT_RETRY_POLICY.maxAttempts, 3);
});
