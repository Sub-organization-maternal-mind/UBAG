import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export interface Settings {
  gatewayUrl: string;
  appSecret: string;
  apiVersion: string;
}

// Default gateway URL: use the page's own origin so production deployments
// (e.g. https://ubag.polytronx.com) work without any manual configuration.
// Falls back to 127.0.0.1:8081 for local development.
function defaultGatewayUrl(): string {
  if (!browser) return 'http://127.0.0.1:8081';
  const stored = localStorage.getItem('ubag_gateway_url');
  if (stored) return stored;
  // In production the dashboard and gateway are on the same origin via nginx
  return window.location.origin;
}

const DEFAULT_SETTINGS: Settings = {
  gatewayUrl: defaultGatewayUrl(),
  appSecret: (browser && localStorage.getItem('ubag_app_secret')) || '',
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
