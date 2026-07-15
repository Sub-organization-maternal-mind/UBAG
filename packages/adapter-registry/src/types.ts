/** JSON value type used across the registry library. */
export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | { [key: string]: JsonValue };

export type JsonObject = { [key: string]: JsonValue };

export type AdapterStatus =
  | 'mock'
  | 'stub'
  | 'experimental'
  | 'beta'
  | 'stable'
  | 'deprecated';

export interface SelectorStrategy {
  readonly type: string;
  readonly drift_baseline_required?: boolean;
}

/** One caller-selectable provider UI setting declared by an adapter manifest. */
export interface CatalogSetting {
  readonly kind: 'choice' | 'toggle';
  readonly values?: readonly string[];
}

/**
 * Declares which provider UI settings a caller may pin per job, keyed by the
 * adapter's own setting keys. An empty `settings` map means nothing is
 * caller-selectable and the operator default always applies.
 */
export interface ModelCatalog {
  readonly settings: { readonly [key: string]: CatalogSetting };
}

/**
 * Known fields of an adapter manifest. Provider-specific extras are preserved
 * via the index signature so the loader never drops data.
 */
export interface AdapterManifest {
  readonly schema_version: string;
  readonly id: string;
  readonly display_name: string;
  readonly version: string;
  readonly status: AdapterStatus;
  readonly supported_command_types: readonly string[];
  readonly capabilities: readonly string[];
  readonly selector_strategy: SelectorStrategy;
  readonly model_catalog?: ModelCatalog;
  readonly aliases?: readonly string[];
  readonly login_posture?: string;
  readonly [key: string]: unknown;
}

export interface DriftMetadata {
  readonly baseline_required: boolean;
  readonly selector_strategy_type: string;
}

/** One entry in the extended registry index. */
export interface RegistryEntry {
  readonly id: string;
  readonly manifest: string;
  readonly version: string;
  readonly status: AdapterStatus;
  readonly capabilities: readonly string[];
  readonly supported_command_types: readonly string[];
  readonly drift: DriftMetadata;
  readonly checksum: string;
  readonly model_catalog?: ModelCatalog;
  readonly signature?: string;
}

export const REGISTRY_INDEX_SCHEMA_VERSION = 'ubag.adapters.index.v1' as const;

/** The extended registry index document. */
export interface RegistryIndex {
  readonly schema_version: typeof REGISTRY_INDEX_SCHEMA_VERSION;
  readonly kind: string;
  readonly description?: string;
  readonly generated_at?: string;
  readonly safe_mode?: JsonObject;
  readonly adapters: readonly RegistryEntry[];
}

/** Result of comparing a stored index against freshly computed checksums. */
export interface DriftReport {
  readonly inSync: boolean;
  readonly added: readonly string[];
  readonly removed: readonly string[];
  readonly changed: readonly DriftChange[];
}

export interface DriftChange {
  readonly id: string;
  readonly expected: string;
  readonly actual: string;
}
