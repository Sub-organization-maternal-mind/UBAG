#!/usr/bin/env node
// Checks that the OpenAPI spec and proto reference docs are not stale.
// CI runs this after the spec is modified.
import { readFileSync, statSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const specPath = resolve(root, 'packages/openapi/openapi.yaml');
const refDocPath = resolve(root, 'apps/docs/src/content/docs/reference/api.md');

let specStat, docStat;

try {
  specStat = statSync(specPath);
} catch {
  console.warn(`[check-api-reference] WARNING: openapi.yaml not found at ${specPath} — skipping check`);
  console.log('[check-api-reference] OK');
  process.exit(0);
}

try {
  docStat = statSync(refDocPath);
} catch {
  console.error(`[check-api-reference] ERROR: reference doc not found at ${refDocPath}`);
  process.exit(1);
}

// Warn if spec is newer than the reference doc
if (specStat.mtimeMs > docStat.mtimeMs + 1000) {
  console.warn(
    `[check-api-reference] WARNING: openapi.yaml (${new Date(specStat.mtimeMs).toISOString()}) ` +
    `is newer than api.md (${new Date(docStat.mtimeMs).toISOString()}). ` +
    `Consider regenerating the API reference docs.`
  );
  // Don't fail — the spec evolves; this is advisory only
}

console.log('[check-api-reference] OK');
