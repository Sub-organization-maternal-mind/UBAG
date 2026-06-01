import type { UbagFetch } from "./client.js";

export const SIDECAR_URL = "http://127.0.0.1:7878";

export interface DiscoverSidecarOptions {
  fetch?: UbagFetch;
  timeoutMs?: number;
}

// discoverSidecar probes the loopback sidecar health endpoint and returns its
// base URL if reachable, else null. Never throws.
export async function discoverSidecar(
  options: DiscoverSidecarOptions = {},
): Promise<string | null> {
  const doFetch = options.fetch ?? (globalThis.fetch as UbagFetch | undefined);
  if (!doFetch) return null;
  const timeoutMs = options.timeoutMs ?? 200;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await doFetch(`${SIDECAR_URL}/v1/health`, { signal: controller.signal });
    return res.status === 200 ? SIDECAR_URL : null;
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}
