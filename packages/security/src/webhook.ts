import { hmacSha256Base64Url, randomTokenSegment, timingSafeEqualText, type SecretInput } from "./crypto.js";

export const WEBHOOK_SIGNATURE_VERSION = "v1";
export const WEBHOOK_SIGNATURE_HEADER = "Ubag-Webhook-Signature";
export const WEBHOOK_TIMESTAMP_HEADER = "Ubag-Webhook-Timestamp";
export const WEBHOOK_NONCE_HEADER = "Ubag-Webhook-Nonce";
export const WEBHOOK_SIGNATURE_BASE_STRING = "timestamp.nonce.body";
export const DEFAULT_WEBHOOK_TOLERANCE_SECONDS = 300;

export interface WebhookSigningInput {
  secret: SecretInput;
  body: string | Uint8Array;
  timestamp?: number;
  nonce?: string;
}

export interface WebhookSignatureHeaders {
  [WEBHOOK_SIGNATURE_HEADER]: string;
  [WEBHOOK_TIMESTAMP_HEADER]: string;
  [WEBHOOK_NONCE_HEADER]: string;
}

export interface WebhookSigningResult {
  signature: string;
  baseString: string;
  timestamp: number;
  nonce: string;
  headers: WebhookSignatureHeaders;
}

export interface WebhookVerificationInput {
  secret: SecretInput;
  body: string | Uint8Array;
  signatureHeader: string | null | undefined;
  timestampHeader: string | null | undefined;
  nonceHeader: string | null | undefined;
  now?: number;
  toleranceSeconds?: number;
  acceptNonce?: (nonce: string, timestamp: number) => boolean;
}

export type WebhookVerificationResult =
  | { ok: true; timestamp: number; nonce: string; signature: string }
  | { ok: false; reason: "missing_header" | "malformed_header" | "stale" | "replay" | "invalid_signature" };

export function signWebhook(input: WebhookSigningInput): WebhookSigningResult {
  const timestamp = input.timestamp ?? Math.floor(Date.now() / 1000);
  const nonce = input.nonce ?? randomTokenSegment(18);
  const baseString = buildWebhookBaseString(timestamp, nonce, input.body);
  const signature = `${WEBHOOK_SIGNATURE_VERSION}=${hmacSha256Base64Url(input.secret, baseString)}`;

  return {
    signature,
    baseString,
    timestamp,
    nonce,
    headers: {
      [WEBHOOK_SIGNATURE_HEADER]: signature,
      [WEBHOOK_TIMESTAMP_HEADER]: String(timestamp),
      [WEBHOOK_NONCE_HEADER]: nonce
    }
  };
}

export function verifyWebhookSignature(input: WebhookVerificationInput): WebhookVerificationResult {
  if (isBlank(input.signatureHeader) || isBlank(input.timestampHeader) || isBlank(input.nonceHeader)) {
    return { ok: false, reason: "missing_header" };
  }

  const timestamp = Number(input.timestampHeader);
  if (!Number.isSafeInteger(timestamp) || timestamp <= 0) {
    return { ok: false, reason: "malformed_header" };
  }

  const parsedSignature = parseSignature(input.signatureHeader);
  if (parsedSignature === null || !isNonce(input.nonceHeader)) {
    return { ok: false, reason: "malformed_header" };
  }

  const now = input.now ?? Math.floor(Date.now() / 1000);
  const toleranceSeconds = input.toleranceSeconds ?? DEFAULT_WEBHOOK_TOLERANCE_SECONDS;
  if (Math.abs(now - timestamp) > toleranceSeconds) {
    return { ok: false, reason: "stale" };
  }

  const expected = signWebhook({
    secret: input.secret,
    body: input.body,
    timestamp,
    nonce: input.nonceHeader
  }).signature;

  if (!timingSafeEqualText(parsedSignature.raw, expected)) {
    return { ok: false, reason: "invalid_signature" };
  }

  if (input.acceptNonce !== undefined && !input.acceptNonce(input.nonceHeader, timestamp)) {
    return { ok: false, reason: "replay" };
  }

  return {
    ok: true,
    timestamp,
    nonce: input.nonceHeader,
    signature: parsedSignature.raw
  };
}

export function buildWebhookBaseString(timestamp: number, nonce: string, body: string | Uint8Array): string {
  const bodyText = typeof body === "string" ? body : Buffer.from(body).toString("utf8");
  return `${timestamp}.${nonce}.${bodyText}`;
}

function parseSignature(value: string): { version: typeof WEBHOOK_SIGNATURE_VERSION; digest: string; raw: string } | null {
  const [version, digest, extra] = value.split("=");
  if (version !== WEBHOOK_SIGNATURE_VERSION || digest === undefined || digest === "" || extra !== undefined) {
    return null;
  }
  if (!/^[A-Za-z0-9_-]+$/.test(digest)) {
    return null;
  }
  return { version, digest, raw: value };
}

function isBlank(value: string | null | undefined): value is null | undefined | "" {
  return value === null || value === undefined || value.trim() === "";
}

function isNonce(value: string): boolean {
  return value.length >= 16 && /^[A-Za-z0-9_-]+$/.test(value);
}
