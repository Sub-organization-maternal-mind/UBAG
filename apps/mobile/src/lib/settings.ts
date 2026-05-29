import { writable } from "svelte/store";

// Non-secret connection preferences. These are safe to persist in plain
// localStorage (URL + UI prefs only). The gateway app-secret is handled
// separately by lib/secureStore.ts and is never stored here.

export interface Settings {
  gatewayUrl: string;
  refreshSeconds: number;
  liveTimeline: boolean;
}

export const DEFAULT_SETTINGS: Settings = {
  gatewayUrl: "http://127.0.0.1:8080",
  refreshSeconds: 15,
  liveTimeline: true,
};

const STORAGE_KEY = "ubag.settings";

function load(): Settings {
  if (typeof localStorage === "undefined") {
    return { ...DEFAULT_SETTINGS };
  }
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return { ...DEFAULT_SETTINGS };
    }
    const parsed = JSON.parse(raw) as Partial<Settings>;
    return {
      gatewayUrl:
        typeof parsed.gatewayUrl === "string" && parsed.gatewayUrl
          ? parsed.gatewayUrl
          : DEFAULT_SETTINGS.gatewayUrl,
      refreshSeconds:
        typeof parsed.refreshSeconds === "number" && parsed.refreshSeconds >= 5
          ? parsed.refreshSeconds
          : DEFAULT_SETTINGS.refreshSeconds,
      liveTimeline:
        typeof parsed.liveTimeline === "boolean"
          ? parsed.liveTimeline
          : DEFAULT_SETTINGS.liveTimeline,
    };
  } catch {
    return { ...DEFAULT_SETTINGS };
  }
}

export const settings = writable<Settings>(load());

settings.subscribe((value) => {
  if (typeof localStorage === "undefined") {
    return;
  }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(value));
  } catch {
    // Storage may be unavailable (private mode); ignore — prefs are best-effort.
  }
});

export function normalizeGatewayUrl(url: string): string {
  const trimmed = url.trim().replace(/\/+$/, "");
  return trimmed.length > 0 ? trimmed : DEFAULT_SETTINGS.gatewayUrl;
}
