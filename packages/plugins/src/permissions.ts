import { PermissionDeniedError, PluginExecutionError } from './errors.ts';
import type { HostFunctionName, PluginJsonValue, PluginManifest } from './manifest.ts';

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface FetchRequest {
  readonly url: string;
  readonly method?: string;
  readonly headers?: Record<string, string>;
  readonly body?: string;
}

export interface FetchResponse {
  readonly status: number;
  readonly headers: Record<string, string>;
  readonly body: string;
}

/**
 * Real implementations of host functions, provided by the gateway integrator.
 *
 * The host runtime (wazero / @wasmer/wasi in production) supplies these; the
 * sandbox only ever calls the implementations a manifest explicitly requested.
 */
export interface HostBindings {
  log?(level: LogLevel, message: string): void;
  clock?(): number;
  random?(): number;
  fetch?(request: FetchRequest): FetchResponse | Promise<FetchResponse>;
  read_file?(path: string): string;
  get_env?(key: string): string | undefined;
}

/** Capability surface handed to the guest for a single invocation. */
export interface GuestContext {
  readonly pluginId: string;
  readonly capability: string;
  readonly deadlineMs: number;
  log(level: LogLevel, message: string): void;
  clock(): number;
  random(): number;
  fetch(request: FetchRequest): FetchResponse | Promise<FetchResponse>;
  readFile(path: string): string;
  getEnv(key: string): string | undefined;
}

function hostnameOf(url: string): string {
  // Avoid the WHATWG URL global to keep this module free of runtime deps.
  const withoutScheme = url.replace(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//, '');
  const authority = withoutScheme.split('/')[0] ?? '';
  const hostAndPort = authority.split('@').pop() ?? '';
  const host = hostAndPort.split(':')[0] ?? '';
  return host.toLowerCase();
}

function hostAllowed(host: string, allowed: readonly string[]): boolean {
  return allowed.some((pattern) => {
    const normalized = pattern.toLowerCase();
    if (normalized.startsWith('*.')) {
      const suffix = normalized.slice(1); // ".example.com"
      return host === normalized.slice(2) || host.endsWith(suffix);
    }
    return host === normalized;
  });
}

function pathAllowed(path: string, allowed: readonly string[]): boolean {
  const normalized = path.replace(/\\/g, '/');
  if (normalized.includes('../')) {
    return false;
  }
  return allowed.some((root) => {
    const normalizedRoot = root.replace(/\\/g, '/').replace(/\/$/, '');
    return normalized === normalizedRoot || normalized.startsWith(`${normalizedRoot}/`);
  });
}

function granted(manifest: PluginManifest, fn: HostFunctionName): boolean {
  return manifest.permissions.host_functions.includes(fn);
}

function requireBinding<T>(value: T | undefined, fn: HostFunctionName, pluginId: string): T {
  if (value === undefined) {
    throw new PluginExecutionError(`host did not provide a binding for "${fn}"`, pluginId);
  }
  return value;
}

export interface BuildGuestContextOptions {
  readonly capability: string;
  readonly now?: () => number;
}

/**
 * Build the default-deny capability context for one guest invocation.
 *
 * Any host function the manifest did not request resolves to a stub that throws
 * {@link PermissionDeniedError}. Granted functions are additionally wrapped to
 * enforce the network/filesystem/env allowlists at call time.
 */
export function buildGuestContext(
  manifest: PluginManifest,
  bindings: HostBindings,
  options: BuildGuestContextOptions,
): GuestContext {
  const { id } = manifest;
  const now = options.now ?? (() => Date.now());
  const deadlineMs = now() + manifest.permissions.max_execution_ms;

  const denied = (fn: HostFunctionName): never => {
    throw new PermissionDeniedError(`host function "${fn}" is not granted to plugin "${id}"`, id);
  };

  return {
    pluginId: id,
    capability: options.capability,
    deadlineMs,
    log(level: LogLevel, message: string): void {
      if (!granted(manifest, 'log')) {
        denied('log');
      }
      requireBinding(bindings.log, 'log', id)(level, message);
    },
    clock(): number {
      if (!granted(manifest, 'clock')) {
        denied('clock');
      }
      return requireBinding(bindings.clock, 'clock', id)();
    },
    random(): number {
      if (!granted(manifest, 'random')) {
        denied('random');
      }
      return requireBinding(bindings.random, 'random', id)();
    },
    fetch(request: FetchRequest): FetchResponse | Promise<FetchResponse> {
      if (!granted(manifest, 'fetch')) {
        denied('fetch');
      }
      if (!manifest.permissions.network.allowed) {
        throw new PermissionDeniedError(`plugin "${id}" has no network permission`, id);
      }
      const host = hostnameOf(request.url);
      if (!hostAllowed(host, manifest.permissions.network.allowed_hosts)) {
        throw new PermissionDeniedError(
          `network egress to "${host}" is not in the allowed_hosts list for plugin "${id}"`,
          id,
        );
      }
      return requireBinding(bindings.fetch, 'fetch', id)(request);
    },
    readFile(path: string): string {
      if (!granted(manifest, 'read_file')) {
        denied('read_file');
      }
      if (!manifest.permissions.filesystem.allowed) {
        throw new PermissionDeniedError(`plugin "${id}" has no filesystem permission`, id);
      }
      if (!pathAllowed(path, manifest.permissions.filesystem.allowed_paths)) {
        throw new PermissionDeniedError(
          `filesystem read of "${path}" is not in the allowed_paths list for plugin "${id}"`,
          id,
        );
      }
      return requireBinding(bindings.read_file, 'read_file', id)(path);
    },
    getEnv(key: string): string | undefined {
      if (!granted(manifest, 'get_env')) {
        denied('get_env');
      }
      if (!manifest.permissions.env.allowed) {
        throw new PermissionDeniedError(`plugin "${id}" has no env permission`, id);
      }
      if (!manifest.permissions.env.allowed_keys.includes(key)) {
        throw new PermissionDeniedError(
          `env key "${key}" is not in the allowed_keys list for plugin "${id}"`,
          id,
        );
      }
      return requireBinding(bindings.get_env, 'get_env', id)(key);
    },
  };
}

export type { PluginJsonValue };
