import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export interface Settings {
  gatewayUrl: string;
  appSecret: string;
  apiVersion: string;
}

const DEFAULT_SETTINGS: Settings = {
  gatewayUrl: (browser && localStorage.getItem('ubag_gateway_url')) || 'http://127.0.0.1:8081',
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
