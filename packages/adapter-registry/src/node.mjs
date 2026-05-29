// Node-backed disk loader for the UBAG adapter registry.
//
// This is the runtime entrypoint (it touches node:fs / node:crypto, which the
// typechecked `src/*.ts` deliberately avoid). It reads adapters/registry.json
// and each manifest, validates them against the bundled schemas, computes
// manifest checksums, and returns a queryable AdapterRegistry plus the extended
// index document.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  AdapterRegistry,
  RegistryError,
  buildRegistryEntry,
  buildRegistryIndex,
  detectDrift,
  formatChecksum,
  validateAdapterManifest,
  validateRegistryIndex,
} from './registry.ts';

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const schemaDir = join(packageRoot, 'schema');

const adapterManifestSchema = readJson(join(schemaDir, 'adapter-manifest.schema.json'));
const registryIndexSchema = readJson(join(schemaDir, 'registry-index.schema.json'));

/** Strip a leading UTF-8 byte-order mark so JSON.parse and checksums are stable. */
function stripBom(text) {
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text;
}

function readJson(path) {
  return JSON.parse(stripBom(readFileSync(path, 'utf8')));
}

/** SHA-256 hex digest of a UTF-8 string. */
export function sha256Hex(text) {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

/**
 * Load the adapter registry from disk.
 *
 * @param {object} options
 * @param {string} options.adaptersDir Absolute path to the `adapters/` directory.
 * @returns {{ registry: AdapterRegistry, index: import('./types.ts').RegistryIndex, records: import('./registry.ts').AdapterRecord[] }}
 */
export function loadAdapterRegistry({ adaptersDir }) {
  const baseRegistryPath = join(adaptersDir, 'registry.json');
  const baseRegistry = readJson(baseRegistryPath);

  if (!Array.isArray(baseRegistry.adapters)) {
    throw new RegistryError('index_invalid', `${baseRegistryPath} has no adapters array`);
  }

  const records = [];
  for (const ref of baseRegistry.adapters) {
    if (typeof ref?.manifest !== 'string') {
      throw new RegistryError('index_invalid', `registry entry "${ref?.id}" has no manifest path`);
    }
    const manifestPath = ref.manifest;
    const manifestText = stripBom(readFileSync(join(adaptersDir, manifestPath), 'utf8'));
    const manifest = JSON.parse(manifestText);

    validateAdapterManifest(manifest, adapterManifestSchema);
    if (ref.id !== manifest.id) {
      throw new RegistryError(
        'manifest_invalid',
        `registry id "${ref.id}" does not match manifest id "${manifest.id}"`,
      );
    }

    const checksum = formatChecksum(sha256Hex(manifestText));
    const entry = buildRegistryEntry(manifestPath, manifest, checksum);
    records.push({ entry, manifest });
  }

  const registry = new AdapterRegistry(records);
  const index = buildRegistryIndex(
    records.map((record) => record.entry),
    {
      kind: 'adapter_registry_index',
      description: baseRegistry.description,
      safeMode: baseRegistry.safe_mode,
    },
  );

  return { registry, index, records };
}

/** Read and validate an extended registry index file. */
export function loadRegistryIndexFile(indexPath) {
  const index = readJson(indexPath);
  validateRegistryIndex(index, registryIndexSchema);
  return index;
}

/**
 * Verify a stored extended index against the live manifests on disk.
 *
 * @returns {import('./types.ts').DriftReport}
 */
export function verifyAgainstDisk({ adaptersDir, indexPath }) {
  const stored = loadRegistryIndexFile(indexPath);
  const { index: fresh } = loadAdapterRegistry({ adaptersDir });
  return detectDrift(stored, fresh.adapters);
}

export { adapterManifestSchema, registryIndexSchema };
