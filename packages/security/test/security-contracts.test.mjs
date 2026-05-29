import assert from "node:assert/strict";
import { test } from "node:test";
import {
  AUDIT_EVENT_NAMES,
  WEBHOOK_NONCE_HEADER,
  WEBHOOK_SIGNATURE_BASE_STRING,
  WEBHOOK_SIGNATURE_HEADER,
  WEBHOOK_TIMESTAMP_HEADER,
  authorize,
  buildWebhookBaseString,
  chainAuditEvent,
  createAuditEvent,
  createAuthzAuditEvent,
  createDeviceTokenRecord,
  evaluateRateLimit,
  fingerprintSecret,
  parseBearerCredential,
  parseDeviceToken,
  permissionMatrix,
  redactAuditMetadata,
  signWebhook,
  verifyAppSecret,
  verifyDeviceToken,
  verifyWebhookSignature
} from "../dist/index.js";

test("app-secret bearer contract validates fingerprints without exposing raw secret storage", () => {
  const secret = "fixture-app-secret-not-real";
  const expectedFingerprint = fingerprintSecret(secret);

  assert.deepEqual(parseBearerCredential(undefined), { status: "missing" });
  assert.deepEqual(parseBearerCredential("Basic abc"), { status: "malformed" });
  assert.deepEqual(parseBearerCredential(`bearer ${secret}`), { status: "present", credential: secret });
  assert.deepEqual(parseBearerCredential(`BEARER ${secret}`), { status: "present", credential: secret });

  const result = verifyAppSecret({
    authorizationHeader: `Bearer ${secret}`,
    expectedFingerprint,
    appId: "app_fixture",
    tenantId: "tenant_fixture",
    scopes: ["job:create"]
  });

  assert.equal(result.ok, true);
  assert.equal(result.principal.type, "app");
  assert.equal(result.principal.credentialKind, "app_secret");

  const lowerCaseScheme = verifyAppSecret({
    authorizationHeader: `bearer ${secret}`,
    expectedFingerprint,
    appId: "app_fixture"
  });
  assert.equal(lowerCaseScheme.ok, true);

  assert.equal(
    verifyAppSecret({
      authorizationHeader: "Bearer wrong-secret",
      expectedFingerprint,
      appId: "app_fixture"
    }).reason,
    "invalid"
  );
});

test("device token contract issues lookup records and verifies only the token secret segment", () => {
  const issued = createDeviceTokenRecord({
    deviceId: "device_fixture",
    tokenId: "AbCdEfGhIjKlMnOp",
    secret: "AbCdEfGhIjKlMnOpQrStUvWxYz012345",
    tenantId: "tenant_fixture",
    scopes: ["job:create"]
  });

  assert.equal(issued.token.startsWith("ubag_dev."), true);
  assert.equal(issued.secretFingerprint.includes("AbCdEf"), false);

  const parts = parseDeviceToken(issued.token);
  assert.deepEqual(parts, {
    tokenId: "AbCdEfGhIjKlMnOp",
    secret: "AbCdEfGhIjKlMnOpQrStUvWxYz012345"
  });

  const verified = verifyDeviceToken({
    authorizationHeader: `Bearer ${issued.token}`,
    lookupToken: (tokenId) => (tokenId === issued.tokenId ? issued : undefined)
  });

  assert.equal(verified.ok, true);
  assert.equal(verified.principal.deviceId, "device_fixture");

  assert.equal(
    verifyDeviceToken({
      authorizationHeader: `Bearer ${issued.token}`,
      lookupToken: () => ({ ...issued, status: "revoked" })
    }).reason,
    "revoked"
  );
});

test("RBAC/ABAC contract denies by default and audits privileged decisions", () => {
  assert.equal(permissionMatrix().viewer.includes("secret:rotate"), false);

  const allowed = authorize({
    actor: {
      id: "user_admin",
      type: "user",
      role: "admin",
      tenantId: "tenant_a",
      strongAuth: true
    },
    action: "secret:rotate",
    tenantId: "tenant_a",
    privacyMode: "standard"
  });
  assert.equal(allowed.effect, "allow");
  assert.equal(allowed.requiredAudit, true);

  const denied = authorize({
    actor: {
      id: "user_viewer",
      type: "user",
      role: "viewer",
      tenantId: "tenant_a"
    },
    action: "secret:rotate",
    tenantId: "tenant_a"
  });
  assert.equal(denied.effect, "deny");
  assert.equal(denied.reason, "role_missing_permission");

  const boundary = authorize({
    actor: {
      id: "svc",
      type: "service",
      role: "service",
      tenantId: "tenant_a"
    },
    action: "job:create",
    tenantId: "tenant_b"
  });
  assert.equal(boundary.effect, "deny");
  assert.equal(boundary.reason, "tenant_boundary_mismatch");

  const auditEvent = createAuthzAuditEvent({
    decision: denied,
    actorId: "user_viewer",
    actorType: "user",
    tenantId: "tenant_a",
    action: "secret:rotate",
    traceId: "trace_fixture",
    source: "security-test"
  });
  assert.equal(auditEvent.name, "authz.deny");
  assert.equal(auditEvent.result, "denied");
});

test("audit metadata redaction and digest chaining avoid secret leakage", () => {
  const redacted = redactAuditMetadata({
    nested: {
      authorization: "Bearer fixture",
      safe_count: 2
    },
    webhook_secret: "fixture"
  });

  assert.equal(redacted.nested.authorization, "[REDACTED]");
  assert.equal(redacted.nested.safe_count, 2);
  assert.equal(redacted.webhook_secret, "[REDACTED]");

  const event = createAuditEvent({
    name: "webhook.secret_rotated",
    actor: { id: "user_admin", type: "user" },
    tenantId: "tenant_a",
    resource: { type: "webhook_endpoint", id: "wh_fixture" },
    action: "webhook:configure",
    result: "success",
    traceId: "trace_fixture",
    source: "security-test",
    metadata: {
      new_secret: "must-not-survive",
      secret_id: "wh_sec_fixture"
    }
  });

  assert.equal(event.metadata.new_secret, "[REDACTED]");
  assert.equal(event.metadata.secret_id, "[REDACTED]");

  const first = chainAuditEvent(event);
  const second = chainAuditEvent(event, first.digest);
  assert.match(first.digest, /^[a-f0-9]{64}$/);
  assert.notEqual(first.digest, second.digest);
});

test("webhook HMAC contract signs timestamp.nonce.body and rejects stale, replayed, or modified deliveries", () => {
  assert.equal(WEBHOOK_SIGNATURE_BASE_STRING, "timestamp.nonce.body");

  const secret = "fixture-webhook-secret-not-real";
  const body = JSON.stringify({ event: "job.completed", job_id: "job_fixture" });
  const timestamp = 1_800_000_000;
  const nonce = "AbCdEfGhIjKlMnOp";
  const signed = signWebhook({ secret, body, timestamp, nonce });

  assert.equal(signed.baseString, buildWebhookBaseString(timestamp, nonce, body));
  assert.equal(signed.headers[WEBHOOK_SIGNATURE_HEADER], signed.signature);
  assert.equal(signed.headers[WEBHOOK_TIMESTAMP_HEADER], String(timestamp));
  assert.equal(signed.headers[WEBHOOK_NONCE_HEADER], nonce);

  const seenNonces = new Set();
  const first = verifyWebhookSignature({
    secret,
    body,
    signatureHeader: signed.signature,
    timestampHeader: String(timestamp),
    nonceHeader: nonce,
    now: timestamp,
    acceptNonce: (value) => {
      if (seenNonces.has(value)) return false;
      seenNonces.add(value);
      return true;
    }
  });
  assert.equal(first.ok, true);

  assert.equal(
    verifyWebhookSignature({
      secret,
      body,
      signatureHeader: signed.signature,
      timestampHeader: String(timestamp),
      nonceHeader: nonce,
      now: timestamp,
      acceptNonce: (value) => !seenNonces.has(value)
    }).reason,
    "replay"
  );

  assert.equal(
    verifyWebhookSignature({
      secret,
      body: `${body} `,
      signatureHeader: signed.signature,
      timestampHeader: String(timestamp),
      nonceHeader: nonce,
      now: timestamp
    }).reason,
    "invalid_signature"
  );

  assert.equal(
    verifyWebhookSignature({
      secret,
      body,
      signatureHeader: signed.signature,
      timestampHeader: String(timestamp),
      nonceHeader: nonce,
      now: timestamp + 301
    }).reason,
    "stale"
  );
});

test("rate-limit contract returns stable allow and reject decisions", () => {
  const policy = {
    id: "tenant_default",
    scope: "tenant",
    limit: 2,
    windowSeconds: 60,
    burst: 1,
    action: "job:create"
  };
  const now = new Date("2026-01-01T00:00:00.000Z");

  const allowed = evaluateRateLimit(policy, { used: 1, resetAt: "2026-01-01T00:01:00.000Z" }, now);
  assert.equal(allowed.allowed, true);
  assert.equal(allowed.status, "allowed");
  assert.equal(allowed.remaining, 1);
  assert.equal(allowed.auditEventName, "rate_limit.allowed");

  const limited = evaluateRateLimit(policy, { used: 3, resetAt: "2026-01-01T00:01:00.000Z" }, now);
  assert.equal(limited.allowed, false);
  assert.equal(limited.status, "limited");
  assert.equal(limited.retryAfterSeconds, 60);
  assert.equal(limited.auditEventName, "rate_limit.rejected");
});

test("security contract registry includes required event families", () => {
  for (const eventName of [
    "auth.app_secret.accepted",
    "auth.device_token.accepted",
    "authz.allow",
    "authz.deny",
    "webhook.delivery_signed",
    "webhook.replay_rejected",
    "rate_limit.allowed",
    "rate_limit.rejected",
    "secret.rotated"
  ]) {
    assert.equal(AUDIT_EVENT_NAMES.includes(eventName), true);
  }
});
