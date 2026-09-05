// verifyWebhookSignature uses Web Crypto (available in Node 19+, browsers, edge
// runtimes) so it compiles without @types/node.

export interface VerifyWebhookOptions {
  timestamp: string;
  nonce: string;
  toleranceSeconds?: number;
}

const DEFAULT_TOLERANCE_SECONDS = 300;
const SIGNATURE_VERSION = "v1";

function base64UrlEncode(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function hmacSha256B64Url(secret: string, message: string): Promise<string> {
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const sig = await crypto.subtle.sign("HMAC", key, enc.encode(message));
  return base64UrlEncode(sig);
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return diff === 0;
}

// verifyWebhookSignature checks the gateway's webhook signature: HMAC-SHA256
// over `${timestamp}.${nonce}.${body}`, base64url-encoded and `v1=`-prefixed
// (gateway internal/webhooks signing.go), within a timestamp tolerance window.
// Returns a Promise<boolean> because Web Crypto is async.
export async function verifyWebhookSignature(
  payload: ArrayBuffer | Uint8Array,
  signature: string,
  secret: string,
  options: VerifyWebhookOptions,
): Promise<boolean> {
  const tolerance = options.toleranceSeconds ?? DEFAULT_TOLERANCE_SECONDS;
  const tsSeconds = Number(options.timestamp);
  if (!Number.isFinite(tsSeconds)) return false;
  const ageSeconds = Math.abs(Date.now() / 1000 - tsSeconds);
  if (ageSeconds > tolerance) return false;

  const body = new TextDecoder().decode(payload);
  const base = `${options.timestamp}.${options.nonce}.${body}`;
  const expected = await hmacSha256B64Url(secret, base);
  return timingSafeEqual(`${SIGNATURE_VERSION}=${expected}`, signature);
}
