import { get } from 'svelte/store';
import { browser } from '$app/environment';
import { settings } from '$lib/stores/settings';
import type { GwResponse } from './types';

function getSettings() {
  return get(settings);
}

export async function gw<T = unknown>(
  method: string,
  path: string,
  body?: unknown
): Promise<GwResponse<T>> {
  if (!browser) return { status: 0, data: null, denied: false, unauthorized: false, error: 'SSR not supported' };

  const s = getSettings();
  const url = s.gatewayUrl.replace(/\/+$/, '') + path;

  const headers: Record<string, string> = {
    'Ubag-Api-Version': s.apiVersion,
    'Content-Type': 'application/json',
  };

  if (s.appSecret) {
    headers['Authorization'] = `Bearer ${s.appSecret}`;
  }

  if (method !== 'GET' && method !== 'HEAD') {
    headers['Idempotency-Key'] = crypto.randomUUID();
  }

  try {
    const response = await fetch(url, {
      method,
      headers,
      body: body != null ? JSON.stringify(body) : undefined,
    });

    const text = await response.text();
    const data = text ? (JSON.parse(text) as T) : null;

    return {
      status: response.status,
      data,
      denied: response.status === 403,
      unauthorized: response.status === 401,
      error: response.ok ? null : `HTTP ${response.status}`,
    };
  } catch (err) {
    return {
      status: -1,
      data: null,
      denied: false,
      unauthorized: false,
      error: err instanceof Error ? err.message : String(err),
    };
  }
}

// Typed convenience wrappers
export const api = {
  get: <T>(path: string) => gw<T>('GET', path),
  post: <T>(path: string, body?: unknown) => gw<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => gw<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => gw<T>('PATCH', path, body),
  delete: <T>(path: string) => gw<T>('DELETE', path),
};
