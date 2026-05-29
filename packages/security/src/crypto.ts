import { createHash, createHmac, randomBytes, timingSafeEqual } from "node:crypto";

export const SHA256_HEX_PREFIX = "sha256:";

export type SecretInput = string | Uint8Array;

export function sha256Hex(input: SecretInput): string {
  return createHash("sha256").update(input).digest("hex");
}

export function fingerprintSecret(input: SecretInput): string {
  return `${SHA256_HEX_PREFIX}${sha256Hex(input)}`;
}

export function verifySecretFingerprint(input: SecretInput, expectedFingerprint: string): boolean {
  if (!expectedFingerprint.startsWith(SHA256_HEX_PREFIX)) {
    return false;
  }
  return timingSafeEqualText(fingerprintSecret(input), expectedFingerprint);
}

export function hmacSha256Base64Url(secret: SecretInput, baseString: string): string {
  return createHmac("sha256", secret).update(baseString).digest("base64url");
}

export function randomTokenSegment(byteLength = 32): string {
  if (!Number.isInteger(byteLength) || byteLength < 16) {
    throw new RangeError("Token segments require at least 16 random bytes.");
  }
  return randomBytes(byteLength).toString("base64url");
}

export function timingSafeEqualText(left: string, right: string): boolean {
  const leftBuffer = Buffer.from(left);
  const rightBuffer = Buffer.from(right);

  if (leftBuffer.byteLength !== rightBuffer.byteLength) {
    timingSafeEqual(leftBuffer, leftBuffer);
    return false;
  }

  return timingSafeEqual(leftBuffer, rightBuffer);
}
