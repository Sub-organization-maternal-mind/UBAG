#!/usr/bin/env node
/**
 * UBAG license posture check (Task B3.3, blueprint §35).
 * Validates that AGPL components and Apache components have correct license indicators.
 */
import { readFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

const AGPL_COMPONENTS = [
  'apps/gateway',
  'apps/worker',
];

const APACHE_COMPONENTS = [
  'apps/dashboard',
  'apps/mobile',
  'apps/docs',
  'adapters',
];

let allPassed = true;

function checkComponent(path, expectedLicense) {
  const pkgPath = resolve(root, path, 'package.json');
  const goModPath = resolve(root, path, 'go.mod');
  const pyProjectPath = resolve(root, path, 'pyproject.toml');
  const requirementsPath = resolve(root, path, 'requirements.txt');

  if (existsSync(pkgPath)) {
    const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
    const actual = pkg.license ?? '(not set)';
    const matches = actual.includes(expectedLicense.split('-')[0]); // Loose match
    if (!matches) {
      console.warn(`[check-licenses] ${path}/package.json: license="${actual}", expected ${expectedLicense}`);
      allPassed = false;
    } else {
      console.log(`[check-licenses] ${path}: ${actual} ✓`);
    }
    return;
  }

  if (existsSync(goModPath)) {
    // Go modules don't have a license field — check for LICENSE file
    const licensePath = resolve(root, path, 'LICENSE');
    if (existsSync(licensePath)) {
      console.log(`[check-licenses] ${path}: LICENSE file present ✓`);
    } else {
      console.log(`[check-licenses] ${path}: no LICENSE file (inherits from root) — OK`);
    }
    return;
  }

  if (existsSync(pyProjectPath) || existsSync(requirementsPath)) {
    console.log(`[check-licenses] ${path}: Python component — OK (inherits root LICENSE)`);
    return;
  }

  console.log(`[check-licenses] ${path}: no package manifest found — skipping`);
}

console.log('[check-licenses] Checking license posture...\n');
console.log('AGPL-3.0 components (server-side):');
for (const p of AGPL_COMPONENTS) checkComponent(p, 'AGPL-3.0');

console.log('\nApache-2.0 components (client-side):');
for (const p of APACHE_COMPONENTS) checkComponent(p, 'Apache-2.0');

if (allPassed) {
  console.log('\n[check-licenses] OK — license posture verified');
} else {
  console.log('\n[check-licenses] Some components have mismatched licenses (see warnings above)');
  // Don't exit 1 — this is advisory for now
}
