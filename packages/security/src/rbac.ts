export const UBAG_ROLES = ["viewer", "developer", "operator", "admin", "superadmin", "support", "service"] as const;
export type UbagRole = (typeof UBAG_ROLES)[number];

export const UBAG_ACTIONS = [
  "job:create",
  "job:read",
  "job:cancel",
  "job:retry",
  "device:enroll",
  "device:revoke",
  "secret:rotate",
  "webhook:configure",
  "webhook:replay",
  "audit:read",
  "rate_limit:manage",
  "role:manage",
  "policy:manage",
  "data:export",
  "support:access"
] as const;
export type UbagAction = (typeof UBAG_ACTIONS)[number];

export type PrivacyMode = "standard" | "hipaa" | "gdpr" | "enterprise";
export type DataClass = "public" | "internal" | "confidential" | "restricted" | "secret";
export type AuthzEffect = "allow" | "deny";

export interface AuthzActor {
  id: string;
  type: "user" | "service" | "device" | "support";
  role: UbagRole;
  tenantId?: string;
  disabled?: boolean;
  strongAuth?: boolean;
}

export interface AuthzResource {
  type: string;
  id: string;
  tenantId?: string;
  ownerId?: string;
  dataClass?: DataClass;
}

export interface AuthzRequest {
  actor?: AuthzActor;
  action: UbagAction;
  resource?: AuthzResource;
  tenantId?: string;
  privacyMode?: PrivacyMode;
  supportReason?: string;
  environment?: "local" | "preview" | "staging" | "production" | "support";
}

export interface AuthzDecision {
  effect: AuthzEffect;
  reason: string;
  auditEventName: "authz.allow" | "authz.deny";
  requiredAudit: boolean;
}

const ROLE_PERMISSIONS: Record<UbagRole, ReadonlySet<UbagAction>> = {
  viewer: new Set(["job:read"]),
  developer: new Set(["job:create", "job:read", "job:cancel", "job:retry", "webhook:configure"]),
  operator: new Set(["job:create", "job:read", "job:cancel", "job:retry", "device:enroll", "device:revoke", "webhook:configure", "webhook:replay", "audit:read"]),
  admin: new Set(["job:create", "job:read", "job:cancel", "job:retry", "device:enroll", "device:revoke", "secret:rotate", "webhook:configure", "webhook:replay", "audit:read", "rate_limit:manage", "role:manage", "data:export"]),
  superadmin: new Set(UBAG_ACTIONS),
  support: new Set(["job:read", "audit:read", "support:access"]),
  service: new Set(["job:create", "job:read", "job:cancel", "job:retry", "webhook:replay"])
};

const PRIVILEGED_ACTIONS = new Set<UbagAction>([
  "device:enroll",
  "device:revoke",
  "secret:rotate",
  "webhook:configure",
  "webhook:replay",
  "audit:read",
  "rate_limit:manage",
  "role:manage",
  "policy:manage",
  "data:export",
  "support:access"
]);

const STRONG_AUTH_ACTIONS = new Set<UbagAction>(["secret:rotate", "role:manage", "policy:manage", "data:export"]);

export function roleHasPermission(role: UbagRole, action: UbagAction): boolean {
  return ROLE_PERMISSIONS[role].has(action);
}

export function authorize(request: AuthzRequest): AuthzDecision {
  const actor = request.actor;
  if (actor === undefined) {
    return deny("actor_missing", request.action);
  }

  if (actor.disabled === true) {
    return deny("actor_disabled", request.action);
  }

  if (!roleHasPermission(actor.role, request.action)) {
    return deny("role_missing_permission", request.action);
  }

  if (STRONG_AUTH_ACTIONS.has(request.action) && actor.strongAuth !== true) {
    return deny("strong_auth_required", request.action);
  }

  const requestedTenant = request.tenantId ?? request.resource?.tenantId;
  if (requestedTenant !== undefined && actor.role !== "superadmin") {
    if (actor.tenantId === undefined) {
      return deny("tenant_boundary_missing", request.action);
    }
    if (actor.tenantId !== requestedTenant) {
      return deny("tenant_boundary_mismatch", request.action);
    }
  }

  if (request.resource?.dataClass === "secret" && request.action !== "secret:rotate" && request.action !== "policy:manage") {
    return deny("secret_data_class_blocked", request.action);
  }

  if ((actor.role === "support" || request.action === "support:access") && isBlank(request.supportReason)) {
    return deny("support_reason_required", request.action);
  }

  if ((request.privacyMode === "hipaa" || request.privacyMode === "gdpr") && request.action === "data:export" && actor.role !== "superadmin") {
    return deny("regulated_export_requires_superadmin", request.action);
  }

  return {
    effect: "allow",
    reason: "allowed",
    auditEventName: "authz.allow",
    requiredAudit: PRIVILEGED_ACTIONS.has(request.action)
  };
}

export function permissionMatrix(): Record<UbagRole, UbagAction[]> {
  const matrix: Partial<Record<UbagRole, UbagAction[]>> = {};
  for (const role of UBAG_ROLES) {
    matrix[role] = [...ROLE_PERMISSIONS[role]];
  }
  return matrix as Record<UbagRole, UbagAction[]>;
}

function deny(reason: string, action: UbagAction): AuthzDecision {
  return {
    effect: "deny",
    reason,
    auditEventName: "authz.deny",
    requiredAudit: PRIVILEGED_ACTIONS.has(action)
  };
}

function isBlank(value: string | undefined): boolean {
  return value === undefined || value.trim() === "";
}
