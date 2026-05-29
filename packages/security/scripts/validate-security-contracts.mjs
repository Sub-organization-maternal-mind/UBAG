import { existsSync, readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import {
  AUDIT_EVENT_NAMES,
  DEFAULT_WEBHOOK_TOLERANCE_SECONDS,
  UBAG_ACTIONS,
  UBAG_ROLES,
  WEBHOOK_NONCE_HEADER,
  WEBHOOK_SIGNATURE_BASE_STRING,
  WEBHOOK_SIGNATURE_HEADER,
  WEBHOOK_SIGNATURE_VERSION,
  WEBHOOK_TIMESTAMP_HEADER,
  permissionMatrix
} from "../dist/index.js";

const failures = [];

function check(label, predicate) {
  try {
    assert.equal(predicate(), true);
  } catch (error) {
    failures.push(`${label}: ${error.message}`);
  }
}

check("webhook signature version is v1", () => WEBHOOK_SIGNATURE_VERSION === "v1");
check("webhook base string is timestamp.nonce.body", () => WEBHOOK_SIGNATURE_BASE_STRING === "timestamp.nonce.body");
check("webhook tolerance is five minutes", () => DEFAULT_WEBHOOK_TOLERANCE_SECONDS === 300);

for (const header of [WEBHOOK_SIGNATURE_HEADER, WEBHOOK_TIMESTAMP_HEADER, WEBHOOK_NONCE_HEADER]) {
  check(`webhook header ${header} uses UBAG namespace`, () => header.startsWith("Ubag-Webhook-"));
}

for (const role of ["viewer", "developer", "operator", "admin", "superadmin", "support", "service"]) {
  check(`role ${role} exported`, () => UBAG_ROLES.includes(role));
}

for (const action of ["secret:rotate", "webhook:replay", "device:enroll", "audit:read", "rate_limit:manage", "role:manage"]) {
  check(`action ${action} exported`, () => UBAG_ACTIONS.includes(action));
}

const matrix = permissionMatrix();
check("viewer cannot rotate secrets", () => !matrix.viewer.includes("secret:rotate"));
check("admin can rotate secrets", () => matrix.admin.includes("secret:rotate"));
check("superadmin can manage policy", () => matrix.superadmin.includes("policy:manage"));
check("support requires support access action only, not secret rotation", () => matrix.support.includes("support:access") && !matrix.support.includes("secret:rotate"));

for (const eventName of [
  "auth.app_secret.accepted",
  "auth.app_secret.rejected",
  "auth.device_token.accepted",
  "authz.allow",
  "authz.deny",
  "webhook.secret_rotated",
  "webhook.delivery_signed",
  "webhook.replay_rejected",
  "rate_limit.allowed",
  "rate_limit.rejected",
  "audit.integrity_digest_created"
]) {
  check(`audit event ${eventName} exported`, () => AUDIT_EVENT_NAMES.includes(eventName));
}

const docsPath = fileURLToPath(new URL("../../../apps/docs/src/content/docs/security/implementation-contracts.md", import.meta.url));
check("security implementation docs exist", () => existsSync(docsPath));
if (existsSync(docsPath)) {
  const docs = readFileSync(docsPath, "utf8");
  const normalizedDocs = docs.toLowerCase();
  for (const term of [
    "@ubag/security",
    WEBHOOK_SIGNATURE_HEADER,
    WEBHOOK_TIMESTAMP_HEADER,
    WEBHOOK_NONCE_HEADER,
    WEBHOOK_SIGNATURE_BASE_STRING,
    "device token",
    "app-secret",
    "RBAC",
    "audit"
  ]) {
    check(`security docs mention ${term}`, () => normalizedDocs.includes(term.toLowerCase()));
  }
}

if (failures.length > 0) {
  console.error(`Security contract validation failed:\n${failures.map((failure) => `- ${failure}`).join("\n")}`);
  process.exit(1);
}

console.log("Security contract validation passed.");
