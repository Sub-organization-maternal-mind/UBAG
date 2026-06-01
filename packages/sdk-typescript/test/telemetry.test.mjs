import { test } from "node:test";
import assert from "node:assert/strict";
import { buildTraceparent, parseTraceparent } from "../dist/telemetry.js";

test("buildTraceparent produces a valid W3C header", () => {
  const tp = buildTraceparent("0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331");
  assert.match(tp, /^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
});

test("parseTraceparent round-trips", () => {
  const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
  const parsed = parseTraceparent(tp);
  assert.equal(parsed.traceId, "0af7651916cd43dd8448eb211c80319c");
  assert.equal(parsed.spanId, "b7ad6b7169203331");
});

test("parseTraceparent rejects malformed input", () => {
  assert.equal(parseTraceparent("garbage"), null);
});
