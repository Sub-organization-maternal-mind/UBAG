// Static consistency gate for the provider adapter surface.
//
// The provider "contract" is split across files that MUST agree; when a
// provider changes its web UI, several of them get edited and it is easy to
// update one side and forget the rest (that class of mistake is why
// selector_drift_detected jobs used to pile up). This checker fails closed on
// every cross-file inconsistency so `pnpm check` catches it before merge:
//
//   1. adapters/registry.json  <-> adapters/<id>/manifest.json (every entry
//      resolves, ids match directories)
//   2. worker REQUIRED_ADAPTER_IDS <-> registry.json
//   3. manifest model_catalog.settings <-> selectors.py ProviderSettings
//      (same keys per provider; every choice desired value is declared in the
//      manifest; every choice value template-reachable)
//   4. selectors.py provider blocks <-> registry (every live web target has a
//      block and vice versa, generic_live_web exempt)
//   5. pinning tests (apps/worker/tests/test_provider_config.py) reference
//      every operator-default desired value
//   6. dashboard LiveBrowser provider shortcuts <-> registered target homepages
//   7. gateway targetCatalog() contains every live target id
//   8. every provider block carries a selector_version
//
// Wired as `check:provider-selectors` in package.json (part of `pnpm check`).
// Companion tools: provider-probe.mjs (live capture) and verify-settings.mjs
// (live canary) — see .codex/skills/provider-refresh/SKILL.md.

import { readFileSync } from 'node:fs';
import {
  LIVE_TARGET_IDS,
  listAdapterManifests,
  liveProvidersBySelectorId,
  repoPath,
} from './lib.mjs';

const failures = [];
const warn = (msg) => console.warn('  warn:', msg);
const fail = (msg) => failures.push(msg);
const read = (rel) => readFileSync(repoPath(rel), 'utf8');

// --- 1. registry <-> manifests ---------------------------------------------
const registry = JSON.parse(read('adapters/registry.json'));
const registryIds = new Set((registry.adapters ?? []).map((a) => a.id));
const adapterEntries = listAdapterManifests();
for (const entry of registry.adapters ?? []) {
  const found = adapterEntries.find((a) => a.id === entry.id);
  if (!found) fail(`registry.json entry "${entry.id}" has no readable manifest (${entry.manifest})`);
  else if (found.manifest?.id && found.manifest.id !== entry.id) {
    fail(`registry.json id "${entry.id}" != manifest id "${found.manifest.id}"`);
  }
}
const adapterDirIds = new Set(adapterEntries.map((a) => a.id));
for (const id of adapterDirIds) {
  if (!registryIds.has(id)) fail(`adapters/${id}/manifest.json exists but is not listed in registry.json`);
}

// --- 2. worker REQUIRED_ADAPTER_IDS <-> registry ----------------------------
const registryPy = read('apps/worker/ubag_worker/adapter_registry.py');
const requiredMatch = /REQUIRED_ADAPTER_IDS = \(([\s\S]*?)\)/.exec(registryPy);
if (!requiredMatch) fail('REQUIRED_ADAPTER_IDS not found in adapter_registry.py');
else {
  const required = [...requiredMatch[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  for (const id of required) {
    if (!registryIds.has(id)) fail(`REQUIRED_ADAPTER_IDS lists "${id}" which is absent from registry.json`);
  }
  for (const id of registryIds) {
    if (!required.includes(id)) fail(`registry.json lists "${id}" which is absent from REQUIRED_ADAPTER_IDS (worker loader hard-fails on this)`);
  }
}

// --- 3-4. selectors.py <-> manifests ----------------------------------------
const selectorProviders = liveProvidersBySelectorId();
const selectorByProviderId = Object.fromEntries(
  Object.values(selectorProviders).map((p) => [p.providerId, p]),
);

for (const id of LIVE_TARGET_IDS) {
  const entry = adapterEntries.find((a) => a.id === id);
  const sel = selectorByProviderId[id];
  if (!entry) { fail(`live target "${id}" has no manifest`); continue; }
  if (!sel) { fail(`live target "${id}" has no ProviderSelectors block in selectors.py`); continue; }
  if (!sel.selectorVersion) fail(`${id}: selector_version missing (every rebase must bump it)`);

  const catalog = entry.manifest?.model_catalog?.settings ?? {};
  const settingKeys = Object.keys(sel.settings);
  // Same key set on both sides (a declared-but-unimplemented setting fails
  // every job; an implemented-but-undeclared one is invisible to the API).
  for (const key of Object.keys(catalog)) {
    if (!settingKeys.includes(key)) fail(`${id}: manifest declares setting "${key}" but selectors.py has no ProviderSetting for it`);
  }
  for (const key of settingKeys) {
    if (!catalog[key]) fail(`${id}: selectors.py applies setting "${key}" but the manifest does not declare it (jobs cannot set it and gateway validation rejects it)`);
  }
  // desired values must be declared choices / sensible toggles
  for (const [key, setting] of Object.entries(sel.settings)) {
    const cat = catalog[key];
    if (!cat) continue;
    if (setting.kind !== cat.kind) fail(`${id}: setting "${key}" kind mismatch (selectors=${setting.kind}, manifest=${cat.kind})`);
    if (cat.kind === 'choice') {
      const values = cat.values ?? [];
      if (setting.desired !== undefined && !values.includes(setting.desired)) {
        fail(`${id}: setting "${key}" desired=${JSON.stringify(setting.desired)} is not in the manifest values [${values.join(', ')}]`);
      }
      if (!values.length) fail(`${id}: setting "${key}" has no values`);
    }
  }
}

for (const sel of Object.values(selectorProviders)) {
  if (sel.providerId === 'generic_live_web') continue;
  if (!registryIds.has(sel.providerId)) {
    fail(`selectors.py defines "${sel.providerId}" which is not in registry.json`);
  }
}

// --- 5. pinning tests reference the operator defaults ------------------------
const pins = read('apps/worker/tests/test_provider_config.py');
for (const id of LIVE_TARGET_IDS) {
  const sel = selectorByProviderId[id];
  if (!sel) continue;
  for (const [key, setting] of Object.entries(sel.settings)) {
    if (setting.desired === undefined) continue;
    const needle = typeof setting.desired === 'string' ? setting.desired : setting.desired ? 'True' : 'False';
    if (!pins.includes(needle)) {
      fail(`${id}: operator default ${key}=${JSON.stringify(setting.desired)} is not pinned in apps/worker/tests/test_provider_config.py (add/update the pinning test)`);
    }
  }
}

// --- 6. dashboard shortcuts <-> registered homepages -------------------------
const liveBrowser = read('apps/dashboard/src/lib/components/LiveBrowser.svelte');
const shortcutMatch = /const providerShortcuts = \[([\s\S]*?)\];/.exec(liveBrowser);
if (!shortcutMatch) fail('providerShortcuts not found in LiveBrowser.svelte');
else {
  const homepages = new Set(
    adapterEntries
      .map((a) => a.manifest?.target_homepage)
      .filter(Boolean)
      .map((h) => h.replace(/\/$/, '')),
  );
  const shortcuts = [...shortcutMatch[1].matchAll(/url: '([^']+)'/g)].map((m) => m[1].replace(/\/$/, ''));
  for (const url of shortcuts) {
    if (!homepages.has(url)) fail(`LiveBrowser.svelte shortcut ${url} does not match any registered adapter target_homepage`);
  }
}

// --- 7. gateway targetCatalog covers every live target -----------------------
const serverGo = read('apps/gateway/internal/httpapi/server.go');
for (const id of LIVE_TARGET_IDS) {
  if (!new RegExp(`"key": "${id}"`).test(serverGo)) {
    fail(`gateway targetCatalog() is missing "${id}" (GET /v1/targets and facade model resolution need it)`);
  }
}

// --- report ------------------------------------------------------------------
if (failures.length) {
  console.error(`Provider selector consistency check FAILED (${failures.length}):`);
  for (const f of failures) console.error('  ✗', f);
  process.exit(1);
}
console.log(
  `Provider selector consistency check passed: ${registryIds.size} adapters, `
  + `${LIVE_TARGET_IDS.length} live targets, manifests <-> selectors <-> pins <-> dashboard <-> gateway all consistent.`,
);
