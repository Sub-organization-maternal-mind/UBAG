import { ManifestValidationError } from './errors.ts';

/** JSON value type passed across the host/guest boundary. */
export type PluginJsonValue =
  | string
  | number
  | boolean
  | null
  | PluginJsonValue[]
  | { [key: string]: PluginJsonValue };

export const PLUGIN_SCHEMA_VERSION = 'ubag.plugin.v0' as const;

export type PluginCapability =
  | 'transform.prompt'
  | 'transform.response'
  | 'hook.job.pre'
  | 'hook.job.post'
  | 'hook.webhook.transform'
  | 'hook.validate'
  | 'adapter.extension'
  | 'command.custom';

export const PLUGIN_CAPABILITIES: readonly PluginCapability[] = [
  'transform.prompt',
  'transform.response',
  'hook.job.pre',
  'hook.job.post',
  'hook.webhook.transform',
  'hook.validate',
  'adapter.extension',
  'command.custom',
];

export type HostFunctionName = 'log' | 'clock' | 'random' | 'fetch' | 'read_file' | 'get_env';

export const HOST_FUNCTION_NAMES: readonly HostFunctionName[] = [
  'log',
  'clock',
  'random',
  'fetch',
  'read_file',
  'get_env',
];

export type EntrypointType = 'wasi-component' | 'wasi-command' | 'core-module';
export type EngineRuntime = 'wasi-preview1' | 'wasi-preview2' | 'core';

export interface PluginEntrypoint {
  readonly type: EntrypointType;
  readonly module: string;
  readonly exports: {
    readonly transform?: string;
    readonly hook?: string;
    readonly init?: string;
  };
}

export interface NetworkPermission {
  readonly allowed: boolean;
  readonly allowed_hosts: readonly string[];
}

export interface FilesystemPermission {
  readonly allowed: boolean;
  readonly allowed_paths: readonly string[];
}

export interface EnvPermission {
  readonly allowed: boolean;
  readonly allowed_keys: readonly string[];
}

export interface PluginPermissions {
  readonly host_functions: readonly HostFunctionName[];
  readonly network: NetworkPermission;
  readonly filesystem: FilesystemPermission;
  readonly env: EnvPermission;
  readonly max_memory_bytes: number;
  readonly max_execution_ms: number;
}

export interface PluginEngine {
  readonly runtime: EngineRuntime;
  readonly min_host_version: string | undefined;
}

export interface PluginManifest {
  readonly schema_version: typeof PLUGIN_SCHEMA_VERSION;
  readonly id: string;
  readonly display_name: string;
  readonly version: string;
  readonly description: string | undefined;
  readonly entrypoint: PluginEntrypoint;
  readonly capabilities: readonly PluginCapability[];
  readonly permissions: PluginPermissions;
  readonly engine: PluginEngine;
}

const ID_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/;
const SEMVER_PATTERN = /^[0-9]+\.[0-9]+\.[0-9]+([-+].+)?$/;
const STRICT_SEMVER_PATTERN = /^[0-9]+\.[0-9]+\.[0-9]+$/;
const DEFAULT_MAX_MEMORY = 16_777_216;
const DEFAULT_MAX_EXECUTION_MS = 1_000;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function pushUnique(issues: string[], message: string): void {
  if (!issues.includes(message)) {
    issues.push(message);
  }
}

function parsePermissionToggle(
  raw: unknown,
  field: string,
  listKey: string,
  issues: string[],
): { allowed: boolean; list: readonly string[] } {
  if (raw === undefined) {
    return { allowed: false, list: [] };
  }
  if (!isRecord(raw)) {
    pushUnique(issues, `permissions.${field} must be an object`);
    return { allowed: false, list: [] };
  }
  const allowed = raw['allowed'] === true;
  if (typeof raw['allowed'] !== 'boolean') {
    pushUnique(issues, `permissions.${field}.allowed must be a boolean`);
  }
  const list: string[] = [];
  const rawList = raw[listKey];
  if (rawList !== undefined) {
    if (!Array.isArray(rawList) || rawList.some((entry) => typeof entry !== 'string')) {
      pushUnique(issues, `permissions.${field}.${listKey} must be a string array`);
    } else {
      list.push(...(rawList as string[]));
    }
  }
  if (!allowed && list.length > 0) {
    pushUnique(
      issues,
      `permissions.${field}.${listKey} is set but permissions.${field}.allowed is false`,
    );
  }
  return { allowed, list };
}

function parsePermissions(raw: unknown, issues: string[]): PluginPermissions {
  if (!isRecord(raw)) {
    pushUnique(issues, 'permissions must be an object');
    raw = {};
  }
  const record = raw as Record<string, unknown>;

  const hostFunctions: HostFunctionName[] = [];
  const rawHostFns = record['host_functions'];
  if (!Array.isArray(rawHostFns)) {
    pushUnique(issues, 'permissions.host_functions must be an array');
  } else {
    for (const entry of rawHostFns) {
      if (typeof entry !== 'string' || !HOST_FUNCTION_NAMES.includes(entry as HostFunctionName)) {
        pushUnique(issues, `permissions.host_functions contains unknown host function "${String(entry)}"`);
        continue;
      }
      if (!hostFunctions.includes(entry as HostFunctionName)) {
        hostFunctions.push(entry as HostFunctionName);
      }
    }
  }

  const network = parsePermissionToggle(record['network'], 'network', 'allowed_hosts', issues);
  const filesystem = parsePermissionToggle(record['filesystem'], 'filesystem', 'allowed_paths', issues);
  const env = parsePermissionToggle(record['env'], 'env', 'allowed_keys', issues);

  if (network.allowed && !hostFunctions.includes('fetch')) {
    pushUnique(issues, 'permissions.network.allowed requires host function "fetch"');
  }
  if (filesystem.allowed && !hostFunctions.includes('read_file')) {
    pushUnique(issues, 'permissions.filesystem.allowed requires host function "read_file"');
  }
  if (env.allowed && !hostFunctions.includes('get_env')) {
    pushUnique(issues, 'permissions.env.allowed requires host function "get_env"');
  }

  const maxMemory = parsePositiveInteger(
    record['max_memory_bytes'],
    'permissions.max_memory_bytes',
    DEFAULT_MAX_MEMORY,
    65_536,
    issues,
  );
  const maxExecution = parsePositiveInteger(
    record['max_execution_ms'],
    'permissions.max_execution_ms',
    DEFAULT_MAX_EXECUTION_MS,
    1,
    issues,
  );

  return {
    host_functions: hostFunctions,
    network: { allowed: network.allowed, allowed_hosts: network.list },
    filesystem: { allowed: filesystem.allowed, allowed_paths: filesystem.list },
    env: { allowed: env.allowed, allowed_keys: env.list },
    max_memory_bytes: maxMemory,
    max_execution_ms: maxExecution,
  };
}

function parsePositiveInteger(
  raw: unknown,
  field: string,
  fallback: number,
  minimum: number,
  issues: string[],
): number {
  if (raw === undefined) {
    return fallback;
  }
  if (typeof raw !== 'number' || !Number.isInteger(raw) || raw < minimum) {
    pushUnique(issues, `${field} must be an integer >= ${minimum}`);
    return fallback;
  }
  return raw;
}

function parseEntrypoint(raw: unknown, issues: string[]): PluginEntrypoint {
  if (!isRecord(raw)) {
    pushUnique(issues, 'entrypoint must be an object');
    return { type: 'wasi-component', module: '', exports: {} };
  }
  const type = raw['type'];
  if (type !== 'wasi-component' && type !== 'wasi-command' && type !== 'core-module') {
    pushUnique(issues, 'entrypoint.type must be one of wasi-component, wasi-command, core-module');
  }
  const moduleValue = raw['module'];
  if (typeof moduleValue !== 'string' || !moduleValue.endsWith('.wasm')) {
    pushUnique(issues, 'entrypoint.module must be a path ending in .wasm');
  }
  const exportsRaw = raw['exports'];
  const exportsOut: { transform?: string; hook?: string; init?: string } = {};
  if (!isRecord(exportsRaw)) {
    pushUnique(issues, 'entrypoint.exports must be an object');
  } else {
    for (const key of ['transform', 'hook', 'init'] as const) {
      const value = exportsRaw[key];
      if (value === undefined) {
        continue;
      }
      if (typeof value !== 'string' || value.length === 0) {
        pushUnique(issues, `entrypoint.exports.${key} must be a non-empty string`);
        continue;
      }
      exportsOut[key] = value;
    }
  }
  return {
    type: (type as EntrypointType) ?? 'wasi-component',
    module: typeof moduleValue === 'string' ? moduleValue : '',
    exports: exportsOut,
  };
}

function parseEngine(raw: unknown, issues: string[]): PluginEngine {
  if (!isRecord(raw)) {
    pushUnique(issues, 'engine must be an object');
    return { runtime: 'wasi-preview2', min_host_version: undefined };
  }
  const runtime = raw['runtime'];
  if (runtime !== 'wasi-preview1' && runtime !== 'wasi-preview2' && runtime !== 'core') {
    pushUnique(issues, 'engine.runtime must be one of wasi-preview1, wasi-preview2, core');
  }
  let minHostVersion: string | undefined;
  const rawMin = raw['min_host_version'];
  if (rawMin !== undefined) {
    if (typeof rawMin !== 'string' || !STRICT_SEMVER_PATTERN.test(rawMin)) {
      pushUnique(issues, 'engine.min_host_version must be a MAJOR.MINOR.PATCH string');
    } else {
      minHostVersion = rawMin;
    }
  }
  return {
    runtime: (runtime as EngineRuntime) ?? 'wasi-preview2',
    min_host_version: minHostVersion,
  };
}

/**
 * Validate and normalize an untrusted manifest object.
 *
 * @throws ManifestValidationError when the manifest violates the schema.
 */
export function parsePluginManifest(input: unknown): PluginManifest {
  const issues: string[] = [];

  if (!isRecord(input)) {
    throw new ManifestValidationError(['manifest must be a JSON object']);
  }

  if (input['schema_version'] !== PLUGIN_SCHEMA_VERSION) {
    pushUnique(issues, `schema_version must be "${PLUGIN_SCHEMA_VERSION}"`);
  }

  const id = input['id'];
  if (typeof id !== 'string' || !ID_PATTERN.test(id)) {
    pushUnique(issues, 'id must match ^[a-z0-9][a-z0-9_-]{1,63}$');
  }

  const displayName = input['display_name'];
  if (typeof displayName !== 'string' || displayName.length === 0) {
    pushUnique(issues, 'display_name must be a non-empty string');
  }

  const version = input['version'];
  if (typeof version !== 'string' || !SEMVER_PATTERN.test(version)) {
    pushUnique(issues, 'version must be a semantic version string');
  }

  let description: string | undefined;
  const rawDescription = input['description'];
  if (rawDescription !== undefined) {
    if (typeof rawDescription !== 'string') {
      pushUnique(issues, 'description must be a string');
    } else {
      description = rawDescription;
    }
  }

  const capabilities: PluginCapability[] = [];
  const rawCapabilities = input['capabilities'];
  if (!Array.isArray(rawCapabilities) || rawCapabilities.length === 0) {
    pushUnique(issues, 'capabilities must be a non-empty array');
  } else {
    for (const entry of rawCapabilities) {
      if (typeof entry !== 'string' || !PLUGIN_CAPABILITIES.includes(entry as PluginCapability)) {
        pushUnique(issues, `capabilities contains unknown capability "${String(entry)}"`);
        continue;
      }
      if (!capabilities.includes(entry as PluginCapability)) {
        capabilities.push(entry as PluginCapability);
      }
    }
  }

  const entrypoint = parseEntrypoint(input['entrypoint'], issues);
  const permissions = parsePermissions(input['permissions'], issues);
  const engine = parseEngine(input['engine'], issues);

  if (issues.length > 0) {
    throw new ManifestValidationError(issues, typeof id === 'string' ? id : undefined);
  }

  return {
    schema_version: PLUGIN_SCHEMA_VERSION,
    id: id as string,
    display_name: displayName as string,
    version: version as string,
    description,
    entrypoint,
    capabilities,
    permissions,
    engine,
  };
}

/** Parse manifest JSON text. */
export function parsePluginManifestJson(text: string): PluginManifest {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (error) {
    throw new ManifestValidationError([
      `manifest is not valid JSON: ${(error as Error).message}`,
    ]);
  }
  return parsePluginManifest(parsed);
}
