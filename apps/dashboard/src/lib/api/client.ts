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
    let data: T | null = null;
    let parseError = false;
    if (text) {
      try {
        data = JSON.parse(text) as T;
      } catch {
        // Non-JSON body (e.g. an HTML error page from a proxy). Don't leak the
        // raw "Unexpected token '<'" exception — surface a clean status-based error.
        parseError = true;
      }
    }

    return {
      status: response.status,
      data,
      denied: response.status === 403,
      unauthorized: response.status === 401,
      error: response.ok
        ? (parseError ? 'Invalid response from gateway' : null)
        : `HTTP ${response.status}`,
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

/**
 * Extract a list array from a gateway response, handling the {data:[]}, {jobs:[]},
 * {items:[]} and named-key envelopes the gateway uses.
 *
 * The response envelope is always GwResponse<unknown>, so `res.data` is the
 * parsed JSON body. The real list lives under one of the well-known keys.
 */
export function listOf<T = unknown>(
  res: { data: unknown },
  ...extraKeys: string[]
): T[] {
  const d = res.data as Record<string, unknown> | null;
  if (!d) return [];
  for (const k of ['data', ...extraKeys, 'items']) {
    if (Array.isArray(d[k])) return d[k] as T[];
  }
  return [];
}

// Typed convenience wrappers
export const api = {
  get: <T>(path: string) => gw<T>('GET', path),
  post: <T>(path: string, body?: unknown) => gw<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => gw<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => gw<T>('PATCH', path, body),
  delete: <T>(path: string) => gw<T>('DELETE', path),
};
