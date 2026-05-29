import { validateSchema, type JsonSchema } from './json-schema.ts';
import {
  REGISTRY_INDEX_SCHEMA_VERSION,
  type AdapterManifest,
  type AdapterStatus,
  type DriftChange,
  type DriftReport,
  type JsonObject,
  type RegistryEntry,
  type RegistryIndex,
} from './types.ts';

export type RegistryErrorCode =
  | 'manifest_invalid'
  | 'index_invalid'
  | 'checksum_format_invalid'
  | 'duplicate_adapter';

export class RegistryError extends Error {
  readonly code: RegistryErrorCode;
  readonly issues: readonly string[];

  constructor(code: RegistryErrorCode, message: string, issues: readonly string[] = []) {
    super(issues.length > 0 ? `${message}: ${issues.join('; ')}` : message);
    this.name = 'RegistryError';
    this.code = code;
    this.issues = issues;
  }
}

const CHECKSUM_PATTERN = /^sha256:[0-9a-f]{64}$/;

/** Format a hex digest as a registry checksum string. */
export function formatChecksum(hex: string): string {
  const normalized = hex.trim().toLowerCase();
  if (!/^[0-9a-f]{64}$/.test(normalized)) {
    throw new RegistryError('checksum_format_invalid', `not a sha-256 hex digest: "${hex}"`);
  }
  return `sha256:${normalized}`;
}

export function isChecksum(value: string): boolean {
  return CHECKSUM_PATTERN.test(value);
}

/** Validate an adapter manifest object against the provided schema. */
export function validateAdapterManifest(
  manifest: unknown,
  schema: JsonSchema,
): asserts manifest is AdapterManifest {
  const issues = validateSchema(schema, manifest);
  if (issues.length > 0) {
    const id =
      typeof manifest === 'object' && manifest !== null && 'id' in manifest
        ? String((manifest as Record<string, unknown>)['id'])
        : 'unknown';
    throw new RegistryError('manifest_invalid', `adapter "${id}" manifest invalid`, issues);
  }
}

/** Validate a registry index document against the provided schema. */
export function validateRegistryIndex(index: unknown, schema: JsonSchema): asserts index is RegistryIndex {
  const issues = validateSchema(schema, index);
  if (issues.length > 0) {
    throw new RegistryError('index_invalid', 'registry index invalid', issues);
  }
}

/** Derive an extended index entry from a validated manifest and its checksum. */
export function buildRegistryEntry(
  manifestPath: string,
  manifest: AdapterManifest,
  checksum: string,
): RegistryEntry {
  if (!isChecksum(checksum)) {
    throw new RegistryError('checksum_format_invalid', `invalid checksum "${checksum}" for ${manifest.id}`);
  }
  return {
    id: manifest.id,
    manifest: manifestPath,
    version: manifest.version,
    status: manifest.status as AdapterStatus,
    capabilities: [...manifest.capabilities],
    supported_command_types: [...manifest.supported_command_types],
    drift: {
      baseline_required: manifest.selector_strategy.drift_baseline_required === true,
      selector_strategy_type: manifest.selector_strategy.type,
    },
    checksum,
  };
}

export interface BuildIndexOptions {
  readonly kind?: string;
  readonly description?: string;
  readonly generatedAt?: string;
  readonly safeMode?: JsonObject;
}

/** Assemble the extended registry index document from entries. */
export function buildRegistryIndex(
  entries: readonly RegistryEntry[],
  options: BuildIndexOptions = {},
): RegistryIndex {
  const seen = new Set<string>();
  for (const entry of entries) {
    if (seen.has(entry.id)) {
      throw new RegistryError('duplicate_adapter', `duplicate adapter id "${entry.id}"`);
    }
    seen.add(entry.id);
  }
  const index: {
    schema_version: typeof REGISTRY_INDEX_SCHEMA_VERSION;
    kind: string;
    description?: string;
    generated_at?: string;
    safe_mode?: JsonObject;
    adapters: readonly RegistryEntry[];
  } = {
    schema_version: REGISTRY_INDEX_SCHEMA_VERSION,
    kind: options.kind ?? 'adapter_registry_index',
    adapters: [...entries].sort((a, b) => a.id.localeCompare(b.id)),
  };
  if (options.description !== undefined) index.description = options.description;
  if (options.generatedAt !== undefined) index.generated_at = options.generatedAt;
  if (options.safeMode !== undefined) index.safe_mode = options.safeMode;
  return index;
}

/**
 * Compare a stored index against freshly computed entries.
 *
 * Drift surfaces as: adapters added, adapters removed, or adapters whose
 * recomputed manifest checksum no longer matches the stored baseline (i.e. the
 * manifest changed without the index being regenerated).
 */
export function detectDrift(
  expected: RegistryIndex,
  actual: readonly RegistryEntry[],
): DriftReport {
  const expectedById = new Map(expected.adapters.map((entry) => [entry.id, entry]));
  const actualById = new Map(actual.map((entry) => [entry.id, entry]));

  const added: string[] = [];
  const removed: string[] = [];
  const changed: DriftChange[] = [];

  for (const [id, entry] of actualById) {
    if (!expectedById.has(id)) {
      added.push(id);
    } else {
      const expectedEntry = expectedById.get(id)!;
      if (expectedEntry.checksum !== entry.checksum) {
        changed.push({ id, expected: expectedEntry.checksum, actual: entry.checksum });
      }
    }
  }
  for (const id of expectedById.keys()) {
    if (!actualById.has(id)) {
      removed.push(id);
    }
  }

  added.sort();
  removed.sort();
  changed.sort((a, b) => a.id.localeCompare(b.id));

  return {
    inSync: added.length === 0 && removed.length === 0 && changed.length === 0,
    added,
    removed,
    changed,
  };
}

export interface AdapterRecord {
  readonly entry: RegistryEntry;
  readonly manifest: AdapterManifest;
}

/** Queryable in-memory adapter registry. */
export class AdapterRegistry {
  readonly #records: readonly AdapterRecord[];
  readonly #byId: Map<string, AdapterRecord>;
  readonly #byAlias: Map<string, AdapterRecord>;

  constructor(records: readonly AdapterRecord[]) {
    const byId = new Map<string, AdapterRecord>();
    const byAlias = new Map<string, AdapterRecord>();
    for (const record of records) {
      if (byId.has(record.entry.id)) {
        throw new RegistryError('duplicate_adapter', `duplicate adapter id "${record.entry.id}"`);
      }
      byId.set(record.entry.id, record);
      byAlias.set(record.entry.id, record);
      for (const alias of record.manifest.aliases ?? []) {
        byAlias.set(alias, record);
      }
    }
    this.#records = records;
    this.#byId = byId;
    this.#byAlias = byAlias;
  }

  list(): readonly RegistryEntry[] {
    return this.#records.map((record) => record.entry);
  }

  records(): readonly AdapterRecord[] {
    return this.#records;
  }

  get(id: string): RegistryEntry | undefined {
    return this.#byId.get(id)?.entry;
  }

  getManifest(id: string): AdapterManifest | undefined {
    return this.#byId.get(id)?.manifest;
  }

  resolve(idOrAlias: string): RegistryEntry | undefined {
    return this.#byAlias.get(idOrAlias)?.entry;
  }

  filterByCapability(capability: string): readonly RegistryEntry[] {
    return this.#records
      .filter((record) => record.manifest.capabilities.includes(capability))
      .map((record) => record.entry);
  }

  filterByStatus(status: AdapterStatus): readonly RegistryEntry[] {
    return this.#records
      .filter((record) => record.entry.status === status)
      .map((record) => record.entry);
  }

  filterByCommandType(commandType: string): readonly RegistryEntry[] {
    return this.#records
      .filter((record) => record.manifest.supported_command_types.includes(commandType))
      .map((record) => record.entry);
  }

  toIndex(options: BuildIndexOptions = {}): RegistryIndex {
    return buildRegistryIndex(
      this.#records.map((record) => record.entry),
      options,
    );
  }
}
