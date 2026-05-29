# @ubag/adapter-registry

Loader, validator, and query library for the UBAG adapter registry. It reads the
existing [`adapters/registry.json`](../../adapters/registry.json) plus each
`adapters/<id>/manifest.json`, validates them against JSON Schemas, computes
manifest checksums, and exposes a queryable registry with drift detection.

It does **not** modify any existing adapter files — it reads them.

## What it adds on top of `adapters/registry.json`

The base `registry.json` is a flat list of `{ id, manifest }`. This package
defines an **extended index** (`ubag.adapters.index.v1`,
[`schema/registry-index.schema.json`](schema/registry-index.schema.json)) that
adds, per adapter:

- `version` and `status` (lifted from the manifest)
- `capabilities` and `supported_command_types` capability flags
- `drift` metadata (`baseline_required`, `selector_strategy_type`)
- `checksum` — `sha256:<hex>` of the raw manifest bytes (BOM-stripped UTF-8)
- `signature` — reserved field for a future detached publisher signature

## Layout

- `src/*.ts` — pure, dependency-free, typechecked logic (types, a minimal
  JSON-Schema-draft-07-subset validator, registry/query/drift functions).
- `src/node.mjs` — the runtime disk loader (`node:fs` + `node:crypto`). Kept out
  of the typechecked surface so `src/*.ts` stays portable.
- `schema/*.json` — the manifest and index JSON Schemas.

## Usage

```js
import { loadAdapterRegistry } from '@ubag/adapter-registry/node';

const { registry, index } = loadAdapterRegistry({ adaptersDir: '/abs/path/to/adapters' });

registry.list();                                // all extended entries
registry.get('chatgpt_web');                    // by id
registry.resolve('chatgpt');                    // by id or alias
registry.filterByCapability('token_streaming'); // capability query
registry.filterByStatus('stub');                // lifecycle query
registry.filterByCommandType('chat.prompt');    // command routing query

// `index` is the publishable ubag.adapters.index.v1 document with checksums.
```

Pure helpers (no Node dependency) are available from the package root:

```js
import { validateAdapterManifest, buildRegistryEntry, detectDrift } from '@ubag/adapter-registry';
```

## Publishing & versioning workflow

1. Author or update `adapters/<id>/manifest.json`. Bump the manifest `version`
   (semver) on any behavioral change.
2. Add the adapter to `adapters/registry.json` (id + manifest path) if new.
3. Regenerate the extended index — the loader computes checksums and capability
   flags from the live manifests:

   ```js
   import { writeFileSync } from 'node:fs';
   import { loadAdapterRegistry } from '@ubag/adapter-registry/node';

   const { index } = loadAdapterRegistry({ adaptersDir });
   writeFileSync('adapter-registry.index.json', JSON.stringify(index, null, 2));
   ```

4. Commit the regenerated index alongside the manifest change.

## Drift-detection workflow

The `checksum` field is the drift baseline. In CI:

```js
import { verifyAgainstDisk } from '@ubag/adapter-registry/node';

const report = verifyAgainstDisk({ adaptersDir, indexPath: 'adapter-registry.index.json' });
if (!report.inSync) {
  // report.added / report.removed / report.changed[] = { id, expected, actual }
  process.exit(1);
}
```

`detectDrift` flags three cases:

- **changed** — a manifest's recomputed checksum no longer matches the committed
  index (manifest edited without regenerating the index, or tampering).
- **added** — an adapter exists on disk but not in the committed index.
- **removed** — the committed index references an adapter no longer on disk.

This catches the common failure where a `manifest.json` selector strategy or
capability changes but the registry index (and downstream consumers) were not
updated.

## Validation guarantees

- Manifests are validated against
  [`schema/adapter-manifest.schema.json`](schema/adapter-manifest.schema.json)
  (tolerant of provider-specific extra fields, strict on the core contract).
- The registry id must equal the manifest `id`.
- The extended index is validated against its schema, including the
  `sha256:<64 hex>` checksum format.

## Commands

```bash
pnpm --filter @ubag/adapter-registry typecheck   # tsc --noEmit over src/**/*.ts
pnpm --filter @ubag/adapter-registry test        # node --test (validates against real adapters/)
```
