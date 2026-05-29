// Secure storage bridge.
//
// The gateway app-secret is a bearer credential and must never be written to
// disk in plaintext, logged, or shipped in the JS bundle. On a Tauri target we
// delegate to native Rust commands (`secure_store_*`) which keep the secret in
// the platform credential store (OS keychain on desktop, Keychain Services on
// iOS; Android keystore is a documented TODO in the Rust layer). When the same
// Svelte code runs in a plain browser during web-only development there is no
// native vault, so we fall back to an in-memory value (cleared on reload) and
// emit a single visible warning. The secret is intentionally NOT persisted to
// localStorage in the web fallback.

const SECRET_KEY = "gateway_app_secret";

let webFallbackSecret: string | null = null;
let warnedAboutFallback = false;

function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

function warnFallbackOnce(): void {
  if (!warnedAboutFallback) {
    warnedAboutFallback = true;
    // Never include the secret value in any log line.
    console.warn(
      "[ubag] Native secure storage unavailable (web dev mode). The gateway " +
        "secret is held in memory only and cleared on reload."
    );
  }
}

async function invokeCommand<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<T>(command, args);
}

export async function setAppSecret(secret: string): Promise<void> {
  if (isTauri()) {
    await invokeCommand<void>("secure_store_set", { key: SECRET_KEY, value: secret });
    return;
  }
  warnFallbackOnce();
  webFallbackSecret = secret;
}

export async function getAppSecret(): Promise<string | null> {
  if (isTauri()) {
    return (await invokeCommand<string | null>("secure_store_get", { key: SECRET_KEY })) ?? null;
  }
  warnFallbackOnce();
  return webFallbackSecret;
}

export async function clearAppSecret(): Promise<void> {
  if (isTauri()) {
    await invokeCommand<void>("secure_store_delete", { key: SECRET_KEY });
    return;
  }
  webFallbackSecret = null;
}

export async function hasAppSecret(): Promise<boolean> {
  const secret = await getAppSecret();
  return !!secret && secret.length > 0;
}
