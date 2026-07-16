import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export interface Settings {
  gatewayUrl: string;
  appSecret: string;
  apiVersion: string;
}

// Local-dev-only defaults baked in at build time (see vite.config.ts `define`).
// Both are undefined in a real production build (no VITE_UBAG_DEFAULT_* set),
// so production behavior below is unchanged. These are NOT real secrets — the
// "app secret" is a fixed placeholder dev token with no access to anything
// beyond this machine's own local gateway; see tools/local-launcher.
declare const __UBAG_DEFAULT_GATEWAY_URL__: string | undefined;
declare const __UBAG_DEFAULT_APP_SECRET__: string | undefined;

// Default gateway URL, in priority order:
//   1. Whatever the user explicitly saved (localStorage) — always wins.
//   2. The local-dev build-time default, when one is baked in.
//   3. The page's own origin, for production (dashboard + gateway share one
//      nginx origin there, so this is correct with zero configuration).
// Without (2), any browser profile that has never saved a value — a fresh
// profile, Incognito, or cleared storage — silently defaulted to the
// dashboard's OWN origin (fallback 3), which is wrong whenever the gateway
// runs on a different port, and looked like the settings kept "resetting."
function defaultGatewayUrl(): string {
  if (!browser) return 'http://127.0.0.1:58080';
  const stored = localStorage.getItem('ubag_gateway_url');
  if (stored) return stored;
  if (typeof __UBAG_DEFAULT_GATEWAY_URL__ !== 'undefined' && __UBAG_DEFAULT_GATEWAY_URL__) {
    return __UBAG_DEFAULT_GATEWAY_URL__;
  }
  return window.location.origin;
}

function defaultAppSecret(): string {
  if (!browser) return '';
  const stored = localStorage.getItem('ubag_app_secret');
  if (stored) return stored;
  if (typeof __UBAG_DEFAULT_APP_SECRET__ !== 'undefined' && __UBAG_DEFAULT_APP_SECRET__) {
    return __UBAG_DEFAULT_APP_SECRET__;
  }
  return '';
}

const DEFAULT_SETTINGS: Settings = {
  gatewayUrl: defaultGatewayUrl(),
  appSecret: defaultAppSecret(),
  apiVersion: '2026-05-22',
};

function createSettingsStore() {
  const { subscribe, set, update } = writable<Settings>(DEFAULT_SETTINGS);
  return {
    subscribe,
    set(s: Settings) {
      if (browser) {
        localStorage.setItem('ubag_gateway_url', s.gatewayUrl);
        localStorage.setItem('ubag_app_secret', s.appSecret);
      }
      set(s);
    },
    update,
    save(url: string, secret: string) {
      this.set({ gatewayUrl: url.replace(/\/+$/, ''), appSecret: secret, apiVersion: '2026-05-22' });
    },
  };
}

export const settings = createSettingsStore();
