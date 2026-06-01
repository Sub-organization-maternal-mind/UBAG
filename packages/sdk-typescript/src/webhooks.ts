// verifyWebhookSignature uses Web Crypto (available in Node 19+, browsers, edge
// runtimes) so it compiles without @types/node.

export interface VerifyWebhookOptions {
  timestamp: string;
  toleranceSeconds?: number;
}

const DEFAULT_TOLERANCE_SECONDS = 300;

function hexEncode(buf: ArrayBuffer): string {
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

async function hmacSha256Hex(secret: string, message: string): Promise<string> {
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const sig = await crypto.subtle.sign("HMAC", key, enc.encode(message));
  return hexEncode(sig);
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

// verifyWebhookSignature checks an HMAC-SHA256 signature over
// `${timestamp}.${body}` and enforces a timestamp tolerance window.
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
  const base = `${options.timestamp}.${body}`;
  const expected = await hmacSha256Hex(secret, base);
  return timingSafeEqual(expected, signature);
}
