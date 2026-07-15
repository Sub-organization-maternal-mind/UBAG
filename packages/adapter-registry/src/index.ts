export type {
  JsonValue,
  JsonObject,
  AdapterStatus,
  SelectorStrategy,
  CatalogSetting,
  ModelCatalog,
  AdapterManifest,
  DriftMetadata,
  RegistryEntry,
  RegistryIndex,
  DriftReport,
  DriftChange,
} from './types.ts';
export { REGISTRY_INDEX_SCHEMA_VERSION } from './types.ts';

export { validateSchema } from './json-schema.ts';
export type { JsonSchema } from './json-schema.ts';

export {
  RegistryError,
  AdapterRegistry,
  formatChecksum,
  isChecksum,
  validateAdapterManifest,
  validateRegistryIndex,
  buildRegistryEntry,
  buildRegistryIndex,
  detectDrift,
} from './registry.ts';
export type {
  RegistryErrorCode,
  BuildIndexOptions,
  AdapterRecord,
} from './registry.ts';
