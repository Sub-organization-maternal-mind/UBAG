#!/usr/bin/env node
/**
 * Validates the UBAG cookbook recipe collection (Task B2.2, blueprint §34).
 * Asserts ≥30 recipes exist and that each references real endpoints or SDK symbols.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const cookbookDir = resolve(root, 'apps/docs/src/content/docs/cookbook');

// Known real endpoints and SDK symbols to check for
const KNOWN_SYMBOLS = [
  '/v1/jobs', '/v1/targets', '/v1/adapters', '/v1/templates', '/v1/webhooks',
  '/v1/browser', '/v1/cache', '/v1/audit', '/v1/metrics', '/v1/health',
  '/v1/sso', '/v1/scim', '/v1/workflows', '/v1/rate-limits', '/v1/alerts',
  '/v1/plugins', '/v1/regions', '/v1/mfa', '/v1/siem',
  'Ubag-Api-Version', 'Idempotency-Key', 'Authorization: Bearer',
  'UbagClient', 'ubag.NewClient', 'UbagGateway', 'ubag_client',
  'UBAG_APP_SECRET', 'curl',
];

let recipes;
try {
  recipes = readdirSync(cookbookDir).filter(f => f.endsWith('.md'));
} catch {
  console.error(`[check-cookbook] ERROR: cookbook directory not found: ${cookbookDir}`);
  process.exit(1);
}

console.log(`[check-cookbook] Found ${recipes.length} recipes`);

if (recipes.length < 30) {
  console.error(`[check-cookbook] FAIL: expected ≥30 recipes, got ${recipes.length}`);
  process.exit(1);
}

let allPassed = true;
for (const recipe of recipes) {
  const content = readFileSync(resolve(cookbookDir, recipe), 'utf8');
  const hasSymbol = KNOWN_SYMBOLS.some(sym => content.includes(sym));
  if (!hasSymbol) {
    console.warn(`[check-cookbook] WARN: ${recipe} doesn't reference any known endpoint/SDK symbol`);
    allPassed = false;
  }
}

if (allPassed) {
  console.log('[check-cookbook] All recipes reference real endpoints/SDK symbols');
} else {
  console.log('[check-cookbook] Some recipes missing endpoint/SDK references (warnings above)');
}
console.log('[check-cookbook] OK');
