import { sha256Hex } from "./crypto.js";
import type { AuthzDecision } from "./rbac.js";

export const AUDIT_EVENT_NAMES = [
  "auth.app_secret.accepted",
  "auth.app_secret.rejected",
  "auth.device_token.accepted",
  "auth.device_token.rejected",
  "authz.allow",
  "authz.deny",
  "device.token_issued",
  "device.token_revoked",
  "secret.rotated",
  "webhook.endpoint_configured",
  "webhook.secret_rotated",
  "webhook.delivery_signed",
  "webhook.delivery_replayed",
  "webhook.verification_failed",
  "webhook.replay_rejected",
  "rate_limit.allowed",
  "rate_limit.rejected",
  "audit.integrity_digest_created",
  "policy.changed",
  "compliance.mode_changed"
] as const;

export type AuditEventName = (typeof AUDIT_EVENT_NAMES)[number];
export type AuditResult = "success" | "failure" | "denied" | "attempted";
export type AuditActorType = "user" | "app" | "device" | "service" | "support" | "system";

export interface AuditActor {
  id: string;
  type: AuditActorType;
}

export interface AuditResource {
  type: string;
  id: string;
}

export interface AuditEventInput {
  name: AuditEventName;
  occurredAt?: string;
  actor: AuditActor;
  tenantId?: string;
  resource?: AuditResource;
  action: string;
  result: AuditResult;
  privacyMode?: string;
  dataClass?: string;
  reason?: string;
  traceId: string;
  source: string;
  metadata?: Record<string, unknown>;
}

export interface AuditEvent extends Required<Omit<AuditEventInput, "resource" | "tenantId" | "privacyMode" | "dataClass" | "reason" | "metadata" | "occurredAt">> {
  occurredAt: string;
  tenantId?: string;
  resource?: AuditResource;
  privacyMode?: string;
  dataClass?: string;
  reason?: string;
  metadata?: Record<string, unknown>;
}

export interface ChainedAuditEvent extends AuditEvent {
  previousDigest?: string;
  digest: string;
}

const SECRET_KEY_PATTERN = /(^|[_-])(authorization|cookie|credential|key|password|private|secret|session|token)([_-]|$)/i;

export function createAuditEvent(input: AuditEventInput): AuditEvent {
  const event: AuditEvent = {
    name: input.name,
    occurredAt: input.occurredAt ?? new Date().toISOString(),
    actor: input.actor,
    action: input.action,
    result: input.result,
    traceId: input.traceId,
    source: input.source
  };

  if (input.tenantId !== undefined) event.tenantId = input.tenantId;
  if (input.resource !== undefined) event.resource = input.resource;
  if (input.privacyMode !== undefined) event.privacyMode = input.privacyMode;
  if (input.dataClass !== undefined) event.dataClass = input.dataClass;
  if (input.reason !== undefined) event.reason = input.reason;
  if (input.metadata !== undefined) event.metadata = redactAuditMetadata(input.metadata);

  return event;
}

export function createAuthzAuditEvent(input: {
  decision: AuthzDecision;
  actorId: string;
  actorType: AuditActorType;
  tenantId?: string;
  resource?: AuditResource;
  action: string;
  traceId: string;
  source: string;
  reason?: string;
}): AuditEvent {
  const eventInput: AuditEventInput = {
    name: input.decision.auditEventName,
    actor: {
      id: input.actorId,
      type: input.actorType
    },
    action: input.action,
    result: input.decision.effect === "allow" ? "success" : "denied",
    traceId: input.traceId,
    source: input.source,
    reason: input.reason ?? input.decision.reason
  };
  if (input.tenantId !== undefined) eventInput.tenantId = input.tenantId;
  if (input.resource !== undefined) eventInput.resource = input.resource;

  return createAuditEvent(eventInput);
}

export function redactAuditMetadata(value: Record<string, unknown>): Record<string, unknown> {
  return redactObject(value) as Record<string, unknown>;
}

export function chainAuditEvent(event: AuditEvent, previousDigest?: string): ChainedAuditEvent {
  const base = previousDigest === undefined ? event : { ...event, previousDigest };
  const digest = sha256Hex(canonicalJson(base));
  const chained: ChainedAuditEvent = { ...event, digest };
  if (previousDigest !== undefined) chained.previousDigest = previousDigest;
  return chained;
}

export function canonicalJson(value: unknown): string {
  return JSON.stringify(sortCanonical(value));
}

function redactObject(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => redactObject(item));
  }

  if (value !== null && typeof value === "object") {
    const output: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value)) {
      output[key] = SECRET_KEY_PATTERN.test(key) ? "[REDACTED]" : redactObject(child);
    }
    return output;
  }

  return value;
}

function sortCanonical(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => sortCanonical(item));
  }

  if (value !== null && typeof value === "object") {
    const output: Record<string, unknown> = {};
    for (const key of Object.keys(value).sort()) {
      const child = (value as Record<string, unknown>)[key];
      if (child !== undefined) {
        output[key] = sortCanonical(child);
      }
    }
    return output;
  }

  return value;
}
