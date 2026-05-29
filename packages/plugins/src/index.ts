export {
  PLUGIN_SCHEMA_VERSION,
  PLUGIN_CAPABILITIES,
  HOST_FUNCTION_NAMES,
  parsePluginManifest,
  parsePluginManifestJson,
} from './manifest.ts';
export type {
  PluginJsonValue,
  PluginCapability,
  HostFunctionName,
  EntrypointType,
  EngineRuntime,
  PluginEntrypoint,
  NetworkPermission,
  FilesystemPermission,
  EnvPermission,
  PluginPermissions,
  PluginEngine,
  PluginManifest,
} from './manifest.ts';

export { buildGuestContext } from './permissions.ts';
export type {
  LogLevel,
  FetchRequest,
  FetchResponse,
  HostBindings,
  GuestContext,
  BuildGuestContextOptions,
} from './permissions.ts';

export { MockWasmExecutor, PluginHost } from './host.ts';
export type {
  HookEvent,
  TransformTarget,
  HookResult,
  GuestModule,
  LoadedGuest,
  WasmExecutor,
  GuestModuleResolver,
  PluginHostOptions,
} from './host.ts';

export {
  PluginError,
  ManifestValidationError,
  PermissionDeniedError,
  CapabilityUnsupportedError,
  PluginExecutionError,
  PluginTimeoutError,
  PluginNotFoundError,
} from './errors.ts';
export type { PluginErrorCode } from './errors.ts';
