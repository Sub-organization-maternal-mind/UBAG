import { test } from "node:test";
import assert from "node:assert/strict";
import { parseSseChunk, isTerminalEvent } from "../dist/streaming.js";

test("parseSseChunk parses a data line into an event", () => {
  const events = parseSseChunk('data: {"type":"token","sequence":1}\n\n');
  assert.equal(events.length, 1);
  assert.equal(events[0].type, "token");
  assert.equal(events[0].sequence, 1);
});

test("parseSseChunk handles multiple events in one chunk", () => {
  const chunk = 'data: {"type":"token","sequence":1}\n\ndata: {"type":"completed","sequence":2}\n\n';
  const events = parseSseChunk(chunk);
  assert.equal(events.length, 2);
});

test("isTerminalEvent recognises terminal types", () => {
  assert.equal(isTerminalEvent({ type: "completed" }), true);
  assert.equal(isTerminalEvent({ type: "failed" }), true);
  assert.equal(isTerminalEvent({ type: "cancelled" }), true);
  assert.equal(isTerminalEvent({ type: "dead_letter" }), true);
  assert.equal(isTerminalEvent({ type: "token" }), false);
});
