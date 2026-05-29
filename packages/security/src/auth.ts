import { fingerprintSecret, randomTokenSegment, verifySecretFingerprint } from "./crypto.js";

export const APP_SECRET_SCHEME = "Bearer";
export const DEVICE_TOKEN_PREFIX = "ubag_dev";

export type BearerCredentialStatus = "missing" | "malformed" | "present";

export interface BearerCredential {
  status: BearerCredentialStatus;
  credential?: string;
}

export interface AppSecretVerificationInput {
  authorizationHeader: string | null | undefined;
  expectedFingerprint: string;
  appId: string;
  tenantId?: string;
  scopes?: readonly string[];
}

export interface AuthenticatedAppPrincipal {
  type: "app";
  appId: string;
  tenantId?: string;
  scopes: readonly string[];
  credentialKind: "app_secret";
}

export type AppSecretVerificationResult =
  | { ok: true; principal: AuthenticatedAppPrincipal }
  | { ok: false; reason: "missing" | "malformed" | "invalid" };

export interface DeviceTokenRecord {
  tokenId: string;
  deviceId: string;
  secretFingerprint: string;
  tenantId?: string;
  appId?: string;
  status: "active" | "revoked" | "expired";
  expiresAt?: string;
  scopes: readonly string[];
}

export interface DeviceTokenParts {
  tokenId: string;
  secret: string;
}

export interface IssuedDeviceToken extends DeviceTokenRecord {
  token: string;
  status: "active";
}

export interface AuthenticatedDevicePrincipal {
  type: "device";
  tokenId: string;
  deviceId: string;
  tenantId?: string;
  appId?: string;
  scopes: readonly string[];
  credentialKind: "device_token";
}

export type DeviceTokenVerificationResult =
  | { ok: true; principal: AuthenticatedDevicePrincipal }
  | { ok: false; reason: "missing" | "malformed" | "unknown" | "revoked" | "expired" | "invalid" };

export function parseBearerCredential(authorizationHeader: string | null | undefined): BearerCredential {
  if (authorizationHeader === null || authorizationHeader === undefined || authorizationHeader.trim() === "") {
    return { status: "missing" };
  }

  const [scheme, credential, extra] = authorizationHeader.trim().split(/\s+/);
  if (scheme === undefined || scheme.toLowerCase() !== APP_SECRET_SCHEME.toLowerCase() || credential === undefined || credential === "" || extra !== undefined) {
    return { status: "malformed" };
  }

  return { status: "present", credential };
}

export function verifyAppSecret(input: AppSecretVerificationInput): AppSecretVerificationResult {
  const parsed = parseBearerCredential(input.authorizationHeader);
  if (parsed.status !== "present") {
    return { ok: false, reason: parsed.status };
  }
  if (parsed.credential === undefined) {
    return { ok: false, reason: "malformed" };
  }

  if (!verifySecretFingerprint(parsed.credential, input.expectedFingerprint)) {
    return { ok: false, reason: "invalid" };
  }

  const principal: AuthenticatedAppPrincipal = {
    type: "app",
    appId: input.appId,
    scopes: input.scopes ?? [],
    credentialKind: "app_secret"
  };
  if (input.tenantId !== undefined) principal.tenantId = input.tenantId;

  return { ok: true, principal };
}

export function createDeviceTokenRecord(input: {
  deviceId: string;
  tokenId?: string;
  secret?: string;
  tenantId?: string;
  appId?: string;
  expiresAt?: string;
  scopes?: readonly string[];
}): IssuedDeviceToken {
  const tokenId = input.tokenId ?? randomTokenSegment(18);
  const secret = input.secret ?? randomTokenSegment(32);
  const token = `${DEVICE_TOKEN_PREFIX}.${tokenId}.${secret}`;

  const record: IssuedDeviceToken = {
    token,
    tokenId,
    deviceId: input.deviceId,
    secretFingerprint: fingerprintSecret(secret),
    status: "active",
    scopes: input.scopes ?? []
  };
  if (input.tenantId !== undefined) record.tenantId = input.tenantId;
  if (input.appId !== undefined) record.appId = input.appId;
  if (input.expiresAt !== undefined) record.expiresAt = input.expiresAt;

  return record;
}

export function parseDeviceToken(token: string): DeviceTokenParts | null {
  const parts = token.split(".");
  if (parts.length !== 3 || parts[0] !== DEVICE_TOKEN_PREFIX) {
    return null;
  }

  const [, tokenId, secret] = parts;
  if (!isBase64Urlish(tokenId) || !isBase64Urlish(secret)) {
    return null;
  }

  return { tokenId, secret };
}

export function verifyDeviceToken(input: {
  authorizationHeader: string | null | undefined;
  lookupToken: (tokenId: string) => DeviceTokenRecord | undefined;
  now?: Date;
}): DeviceTokenVerificationResult {
  const parsedBearer = parseBearerCredential(input.authorizationHeader);
  if (parsedBearer.status !== "present") {
    return { ok: false, reason: parsedBearer.status };
  }
  if (parsedBearer.credential === undefined) {
    return { ok: false, reason: "malformed" };
  }

  const parsedToken = parseDeviceToken(parsedBearer.credential);
  if (parsedToken === null) {
    return { ok: false, reason: "malformed" };
  }

  const record = input.lookupToken(parsedToken.tokenId);
  if (record === undefined) {
    return { ok: false, reason: "unknown" };
  }

  if (record.status === "revoked") {
    return { ok: false, reason: "revoked" };
  }

  const now = input.now ?? new Date();
  if (record.status === "expired" || (record.expiresAt !== undefined && Date.parse(record.expiresAt) <= now.getTime())) {
    return { ok: false, reason: "expired" };
  }

  if (!verifySecretFingerprint(parsedToken.secret, record.secretFingerprint)) {
    return { ok: false, reason: "invalid" };
  }

  const principal: AuthenticatedDevicePrincipal = {
    type: "device",
    tokenId: record.tokenId,
    deviceId: record.deviceId,
    scopes: record.scopes,
    credentialKind: "device_token"
  };
  if (record.tenantId !== undefined) principal.tenantId = record.tenantId;
  if (record.appId !== undefined) principal.appId = record.appId;

  return { ok: true, principal };
}

function isBase64Urlish(value: string | undefined): value is string {
  return value !== undefined && value.length >= 16 && /^[A-Za-z0-9_-]+$/.test(value);
}
