// Shared helpers for the provider-refresh tooling (provider-probe.mjs,
// check-provider-selectors.mjs) and the provider-refresh skill
// (.codex/skills/provider-refresh/SKILL.md).
//
// The provider "source of truth" is two files that must agree:
//   1. adapters/<id>/manifest.json  -> model_catalog.settings  (what jobs MAY set;
//      the gateway validates against this and derives GET /v1/openai/models)
//   2. apps/worker/ubag_worker/live/selectors.py -> ProviderSelectors /
//      ProviderSetting  (how those settings are actually clicked in the live UI,
//      plus the operator-default `desired` values)
// Everything here parses those two shapes so the tools never drift apart on
// their own parsing rules.

import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
);

export const repoPath = (...parts) => path.join(repoRoot, ...parts);

export function readFileSyncIfPresent(file) {
  return existsSync(file) ? readFileSync(file, 'utf8') : null;
}

const SELECTORS_REL = path.join('apps', 'worker', 'ubag_worker', 'live', 'selectors.py');

/** Every adapter listed in adapters/registry.json with its parsed manifest. */
export function listAdapterManifests() {
  const registry = JSON.parse(readFileSync(repoPath('adapters', 'registry.json'), 'utf8'));
  const out = [];
  for (const entry of registry.adapters ?? []) {
    const manifestPath = repoPath('adapters', entry.manifest ?? `${entry.id}/manifest.json`);
    if (!existsSync(manifestPath)) continue;
    let manifest;
    try {
      manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    } catch (err) {
      out.push({ id: entry.id, manifestPath, error: String(err) });
      continue;
    }
    out.push({ id: entry.id, manifestPath, manifest });
  }
  return out;
}

/**
 * Parse apps/worker/ubag_worker/live/selectors.py into per-provider records.
 *
 * Returns { [pythonConstName]: {
 *   providerId, displayName, selectorVersion, targetUrl,
 *   groups: { [groupName]: { candidates: string[] } },
 *   settings: { [key]: { kind, desired } },
 *   blockFound: true,
 * } }
 *
 * The parser is line/indentation-based on purpose: selectors.py is a code
 * file with `SelectorGroup(` and `ProviderSetting(` blocks whose string
 * arguments sit on their own lines. It only needs enough structure for
 * verification — the Python side remains the executor.
 */
export function parseSelectorProviders(src = readFileSync(repoPath(SELECTORS_REL), 'utf8')) {
  const providers = {};
  const blockRe = /^([A-Z][A-Z_0-9]*) = ProviderSelectors\(/gm;
  let m;
  while ((m = blockRe.exec(src)) !== null) {
    const constName = m[1];
    const body = balancedBlock(src, m.index + m[0].length - 1);
    const providerId = matchStr(body, 'provider_id');
    const record = {
      constName,
      providerId,
      displayName: matchStr(body, 'display_name'),
      selectorVersion: matchStr(body, 'selector_version'),
      targetUrl: matchStr(body, 'target_url'),
      groups: parseGroups(body),
      settings: parseSettings(body),
      blockFound: true,
    };
    providers[constName] = record;
  }
  return providers;
}

/** providerId -> record, skipping the internal generic template. */
export function liveProvidersBySelectorId(src) {
  const all = parseSelectorProviders(src);
  const out = {};
  for (const record of Object.values(all)) {
    if (record.providerId && record.providerId !== 'generic_live_web') {
      out[record.providerId] = record;
    }
  }
  return out;
}

/** Content of the parenthesized block that opens at `openIdx` (a '(' char). */
function balancedBlock(src, openIdx) {
  let depth = 0;
  for (let i = openIdx; i < src.length; i++) {
    const c = src[i];
    if (c === '(') depth++;
    else if (c === ')') {
      depth--;
      if (depth === 0) return src.slice(openIdx + 1, i);
    }
  }
  return src.slice(openIdx + 1);
}

function matchStr(body, kw) {
  const m = new RegExp(`\\b${kw}="((?:[^"\\\\]|\\\\.)*)"`).exec(body);
  return m ? m[1] : undefined;
}

function parseGroups(body) {
  const groups = {};
  // SelectorGroup(\n  "name",\n  (\n      "candidate",\n  ),\n) — candidates are the
  // deeper-indented quoted lines inside the tuple paren.
  const groupRe = /SelectorGroup\(\s*\n\s*"([^"]+)",\s*\n\s*\(([\s\S]*?)\),\s*\n\s*\)/g;
  let g;
  while ((g = groupRe.exec(body)) !== null) {
    const name = g[1];
    const candidates = [...g[2].matchAll(/^\s+"((?:[^"\\]|\\.)*)",?\s*$/gm)].map(
      (c) => unpy(c[1]),
    );
    if (!groups[name]) groups[name] = { candidates };
  }
  return groups;
}

function parseSettings(body) {
  const settings = {};
  const settingRe = /ProviderSetting\(/g;
  let s;
  while ((s = settingRe.exec(body)) !== null) {
    const block = balancedBlock(body, s.index + s[0].length - 1);
    // Guard against nested matches (there are none today, but stay safe).
    const key = matchStr(block, 'key');
    if (!key || settings[key]) continue;
    const kindM = /\bkind="([^"]+)"/.exec(block);
    let desired;
    const desiredStr = matchStr(block, 'desired');
    if (desiredStr !== undefined) desired = unpy(desiredStr);
    else if (/\bdesired=True\b/.test(block)) desired = true;
    else if (/\bdesired=False\b/.test(block)) desired = false;
    let required;
    if (/required=True/.test(block)) required = true;
    else if (/required=False/.test(block)) required = false;
    const settings_ = {
      kind: kindM ? kindM[1] : undefined,
      desired,
      required: required ?? true,
      satisfied_when: matchStr(block, 'satisfied_when') !== undefined
        ? unpy(matchStr(block, 'satisfied_when'))
        : undefined,
      // Field names mirror the Python kwargs so the verify tool reads like
      // the engine code it replays.
      open_steps: parseStringTuples(namedTuple(block, 'open_steps')),
      on_when: parseStringList(namedTuple(block, 'on_when')),
      toggle_click: parseStringList(namedTuple(block, 'toggle_click')),
      apply_click: matchStr(block, 'apply_click') !== undefined
        ? unpy(matchStr(block, 'apply_click'))
        : undefined,
    };
    settings[key] = settings_;
  }
  return settings;
}

/** The parenthesized payload of `name=( ... )` inside a block, or null. */
function namedTuple(block, name) {
  const m = new RegExp(`\\b${name}=\\(`).exec(block);
  if (!m) return null;
  return balancedBlock(block, m.index + m[0].length - 1);
}

/** Tuple-of-tuples of quoted strings -> string[][] (open_steps shape). */
function parseStringTuples(payload) {
  if (payload === null) return [];
  const groups = [];
  const tupleRe = /\(([^()]*)\)/g;
  let m;
  while ((m = tupleRe.exec(payload)) !== null) {
    const strs = [...m[1].matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((x) => unpy(x[1]));
    if (strs.length) groups.push(strs);
  }
  return groups;
}

/** Flat tuple of quoted strings -> string[]. */
function parseStringList(payload) {
  if (payload === null) return [];
  return [...payload.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((x) => unpy(x[1]));
}

function unpy(s) {
  // Python double-quoted string escapes we care about: \" and \\.
  return s.replace(/\\"/g, '"').replace(/\\\\/g, '\\');
}

/**
 * True when a selectors.py candidate is pure CSS (checkable with
 * document.querySelectorAll in a raw CDP evaluate). Playwright-only engines
 * (:has-text, text=, >>, :visible) are reported separately instead of being
 * silently "unmatched".
 */
export function isPureCssSelector(sel) {
  return !(
    /^text=/.test(sel) ||
    /:has-text\(/.test(sel) ||
    /:visible/.test(sel) ||
    />>/.test(sel) ||
    /^xpath=/.test(sel)
  );
}

export const LIVE_TARGET_IDS = [
  'chatgpt_web',
  'deepseek_web',
  'gemini_web',
  'mistral_lechat',
  'perplexity_web',
  'duckai_web',
];
