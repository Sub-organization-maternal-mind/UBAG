// Cross-checks the perf + shared-fleet flag docs against the code (slice P8.1).
//
//   node tools/flag-graduation-check.mjs        (also: pnpm check:flag-graduation)
//
// Sources: docs/perf-fleet/FLAGS.md (inventory), docs/perf-fleet/ROLLOUT.md (per-flag checklists),
// deploy/vps/env.example, docker-compose.vps.yml, and the Go/Python sources under apps/.
// Fails on: a program flag missing from the inventory; a `Read in` file that does not mention the
// flag; a `Compose` column that disagrees with docker-compose.vps.yml; an inert-by-default flag that
// compose defaults to on; a `managed` flag without all four checklist steps; a flag missing from
// env.example; a UBAG_* name in the docs that exists nowhere in the code. It reads files only and
// never touches production or flips anything.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

// BINDING.md section 3 rule 5: the program's flag taxonomy. Each must be in the inventory.
export const TAXONOMY = [
  'UBAG_WORKER_STREAM_EVENTS', 'UBAG_WORKER_STREAM_INGEST', 'UBAG_WORKER_STRICT_STREAM_END', 'UBAG_WORKER_STRICT_SUBMIT',
  'UBAG_WORKER_POOL_SIZE', 'UBAG_WORKER_ATTEMPT_EVENT_IDS', 'UBAG_EVENT_NOTIFY', 'UBAG_HELPER_NODES', 'UBAG_HELPER_PLANE',
  'UBAG_HELPER_DISPATCH', 'UBAG_HELPER_VOICE', 'UBAG_FILESPOOL_HONOR_NOT_BEFORE', 'UBAG_REDACT_REMOTE_ENDPOINT',
  'UBAG_VOICE_RECONCILER_FAIL_CLOSED',
];
// Look like flags in the shards but are not env switches (explained in FLAGS.md notes).
export const NOT_FLAGS = ['UBAG_QUEUE_REASONS', 'UBAG_SPOOL_PRIORITY_LANES', 'UBAG_VOICE_HELPER_MEDIA'];
export const STEPS = ['Live-DOM verification', 'Canary criteria', 'Rollback', 'Ledger'];
const INERT = new Set(['off', 'legacy', 'unset']);
const TRUTHY = new Set(['1', 'true', 'yes', 'on', 'local', 'namespaced']);

export function parseFlags(md) {
  const rows = [];
  for (const line of md.split('\n')) {
    const m = line.match(/^\| `(UBAG_[A-Z0-9_]+)` \| ([^|]+) \| ([^|]+) \| ([^|]+) \| (managed|live|knob) \| `([^`]+)` \|\s*$/);
    if (m) rows.push({ name: m[1], side: m[2].trim(), def: m[3].trim(), compose: m[4].trim(), grad: m[5], readIn: m[6] });
  }
  return rows;
}

// Sections are `### \`UBAG_A\`, \`UBAG_B\`` headings; returns Map flag -> section body.
export function parseRollout(md) {
  const out = new Map();
  const parts = md.split(/^### /m).slice(1);
  for (const p of parts) {
    const nl = p.indexOf('\n');
    const names = [...p.slice(0, nl).matchAll(/`(UBAG_[A-Z0-9_]+)`/g)].map((m) => m[1]);
    const body = p.slice(nl + 1).split(/^## /m)[0];
    for (const n of names) out.set(n, body);
  }
  return out;
}

export function parseCompose(text) {
  const out = new Map(); // name -> 'empty' | literal default | 'literal:<v>'
  for (const line of text.split('\n')) {
    const m = line.match(/^\s+(UBAG_[A-Z0-9_]+):\s*(.*?)\s*$/);
    if (!m) continue;
    const sub = m[2].match(/^"?\$\{[A-Z0-9_]+(?::?-([^}]*))?\}"?$/);
    const value = sub ? (sub[1] ?? '') : m[2].replace(/^"|"$/g, '');
    out.set(m[1], value === '' ? 'empty' : value);
  }
  return out;
}

export function checkFlags({ flagsMd, rolloutMd, envExample, composeText, readFile, codeTokens }) {
  const fail = [];
  const rows = parseFlags(flagsMd);
  const byName = new Map(rows.map((r) => [r.name, r]));
  if (byName.size !== rows.length) fail.push('FLAGS.md has a duplicate flag row');
  for (const f of TAXONOMY) if (!byName.has(f)) fail.push(`${f}: in the program taxonomy but missing from FLAGS.md`);
  for (const f of NOT_FLAGS) if (byName.has(f)) fail.push(`${f}: listed as a flag but it is not an env switch`);

  const compose = parseCompose(composeText);
  const sections = parseRollout(rolloutMd);
  const envNames = new Set([...envExample.matchAll(/^#?\s*(UBAG_[A-Z0-9_]+)=/gm)].map((m) => m[1]));

  for (const r of rows) {
    const src = readFile(r.readIn);
    if (src === null) fail.push(`${r.name}: Read in file ${r.readIn} does not exist`);
    else if (!new RegExp(`${r.name}\\b`).test(src)) fail.push(`${r.name}: ${r.readIn} does not mention it`);

    const actual = compose.has(r.name) ? compose.get(r.name) : 'none';
    if (r.compose === 'none' || r.compose === 'empty') {
      if (actual !== r.compose) fail.push(`${r.name}: FLAGS.md Compose says ${r.compose}, docker-compose.vps.yml has ${actual}`);
    } else if (actual !== r.compose) {
      fail.push(`${r.name}: FLAGS.md Compose says ${r.compose}, docker-compose.vps.yml has ${actual}`);
    }
    if (INERT.has(r.def) && TRUTHY.has(String(actual).toLowerCase())) {
      fail.push(`${r.name}: default is ${r.def} but docker-compose.vps.yml defaults it on (${actual}); a graduation must not hide in the compose default`);
    }
    if (!envNames.has(r.name)) fail.push(`${r.name}: missing from deploy/vps/env.example`);

    if (r.grad === 'managed') {
      const body = sections.get(r.name);
      if (body === undefined) fail.push(`${r.name}: managed but has no ROLLOUT.md section`);
      else for (const s of STEPS) {
        const m = body.match(new RegExp(`^- \\*\\*${s}:\\*\\* *(\\S.*)$`, 'm'));
        if (!m) fail.push(`${r.name}: ROLLOUT.md section lacks a non-empty "${s}" step`);
      }
    }
  }
  for (const [name] of sections) {
    const r = byName.get(name);
    if (!r) fail.push(`${name}: has a ROLLOUT.md section but is not in FLAGS.md`);
    else if (r.grad !== 'managed') fail.push(`${name}: has a ROLLOUT.md section but FLAGS.md says ${r.grad}`);
  }

  const known = new Set([...byName.keys(), ...NOT_FLAGS]);
  for (const [label, text] of [['FLAGS.md', flagsMd], ['ROLLOUT.md', rolloutMd]]) {
    for (const t of new Set(text.match(/UBAG_[A-Z0-9]+(?:_[A-Z0-9]+)*/g) ?? [])) {
      if (!known.has(t) && !codeTokens.has(t)) fail.push(`${label}: ${t} appears in no code, compose or deploy file`);
    }
  }
  return { failures: fail, flags: rows.length, managed: rows.filter((r) => r.grad === 'managed').length };
}

function walk(dir, exts, out) {
  for (const n of readdirSync(dir)) {
    if (['node_modules', '.git', 'target', 'dist', 'tests', 'testdata'].includes(n)) continue;
    const p = join(dir, n);
    if (statSync(p).isDirectory()) walk(p, exts, out);
    // this checker and its test name flags themselves, so they cannot count as evidence
    else if (exts.some((e) => n.endsWith(e)) && !n.endsWith('_test.go') && !n.startsWith('flag-graduation-check')) out.push(p);
  }
}

export function collectCodeTokens(root) {
  const files = [];
  walk(join(root, 'apps'), ['.go', '.py'], files);
  walk(join(root, 'deploy'), ['.yml', '.yaml', '.example', '.env', '.sh', '.py'], files);
  walk(join(root, 'tools'), ['.mjs'], files);
  files.push(join(root, 'docker-compose.vps.yml'));
  const tokens = new Set();
  for (const f of files) for (const t of readFileSync(f, 'utf8').match(/UBAG_[A-Z0-9]+(?:_[A-Z0-9]+)*/g) ?? []) tokens.add(t);
  return tokens;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..');
  const read = (p) => readFileSync(join(root, p), 'utf8');
  const { failures, flags, managed } = checkFlags({
    flagsMd: read('docs/perf-fleet/FLAGS.md'),
    rolloutMd: read('docs/perf-fleet/ROLLOUT.md'),
    envExample: read('deploy/vps/env.example'),
    composeText: read('docker-compose.vps.yml'),
    readFile: (p) => { try { return read(p); } catch { return null; } },
    codeTokens: collectCodeTokens(root),
  });
  if (failures.length) {
    console.error(`flag-graduation-check: ${failures.length} problem(s)`);
    for (const f of failures) console.error(`  - ${f}`);
    process.exit(1);
  }
  console.log(`flag-graduation-check: ok (${flags} flags, ${managed} managed checklists)`);
}
