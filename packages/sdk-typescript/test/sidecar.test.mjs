import { test } from "node:test";
import assert from "node:assert/strict";
import { discoverSidecar, SIDECAR_URL } from "../dist/sidecar.js";

test("discoverSidecar returns sidecar URL when probe succeeds", async () => {
  const okFetch = async () => new Response("{}", { status: 200 });
  const url = await discoverSidecar({ fetch: okFetch, timeoutMs: 50 });
  assert.equal(url, SIDECAR_URL);
});

test("discoverSidecar returns null when probe fails", async () => {
  const failFetch = async () => { throw new Error("ECONNREFUSED"); };
  const url = await discoverSidecar({ fetch: failFetch, timeoutMs: 50 });
  assert.equal(url, null);
});

test("discoverSidecar returns null on non-200", async () => {
  const badFetch = async () => new Response("", { status: 503 });
  const url = await discoverSidecar({ fetch: badFetch, timeoutMs: 50 });
  assert.equal(url, null);
});
