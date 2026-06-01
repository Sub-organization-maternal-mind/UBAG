import { test } from "node:test";
import assert from "node:assert/strict";
import { OfflineQueue } from "../dist/offline.js";

function memoryAdapter() {
  let entries = [];
  return {
    read: () => entries,
    write: (e) => { entries = e; },
    _dump: () => entries,
  };
}

test("enqueue persists an entry", () => {
  const adapter = memoryAdapter();
  const q = new OfflineQueue(adapter);
  q.enqueue({ target: "mock", command_type: "submit", input: {} });
  assert.equal(adapter._dump().length, 1);
});

test("flush drains entries through the sender and clears the queue", async () => {
  const adapter = memoryAdapter();
  const q = new OfflineQueue(adapter);
  q.enqueue({ target: "mock", command_type: "submit", input: {} });
  q.enqueue({ target: "mock", command_type: "submit", input: {} });
  const sent = [];
  await q.flush(async (req) => { sent.push(req); });
  assert.equal(sent.length, 2);
  assert.equal(adapter._dump().length, 0);
});

test("flush stops and retains entries when the sender throws", async () => {
  const adapter = memoryAdapter();
  const q = new OfflineQueue(adapter);
  q.enqueue({ target: "mock", command_type: "submit", input: {} });
  await assert.rejects(q.flush(async () => { throw new Error("offline"); }));
  assert.equal(adapter._dump().length, 1);
});
