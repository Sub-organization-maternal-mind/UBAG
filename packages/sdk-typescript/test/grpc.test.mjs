import { test } from "node:test";
import assert from "node:assert/strict";
import { grpcStatusToUbagCode, UbagGrpcClient } from "../dist/grpc.js";

test("grpcStatusToUbagCode maps known statuses", () => {
  assert.equal(grpcStatusToUbagCode(8), "UBAG-QUOTA-RESOURCE-EXHAUSTED-001");
  assert.equal(grpcStatusToUbagCode(4), "UBAG-QUEUE-DEADLINE-001");
  assert.equal(grpcStatusToUbagCode(16), "UBAG-AUTH-UNAUTHENTICATED-001");
});

test("UbagGrpcClient stores host and credentials", () => {
  const client = new UbagGrpcClient({ host: "localhost:50051" });
  assert.equal(client.host, "localhost:50051");
});
