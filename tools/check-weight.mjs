// UBAG weight guardrail: reports bundle/binary/workspace weight vs budgets.
// Warn-only by default; --strict exits non-zero on any budget breach (CI gate).
import { readdirSync, statSync, existsSync, readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const STRICT = process.argv.includes('--strict');
const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const budgets = [
  { name: 'dashboard dist total', path: 'apps/dashboard/dist', max: 1000 * 1024, kind: 'dir' },
  // Initial-load JS = entry chunks + chunks imported by >1 route (chart.js
  // and xterm are single-route lazy chunks and correctly excluded).
  { name: 'dashboard initial JS', path: 'apps/dashboard/dist/_app/immutable', max: 150 * 1024, kind: 'shared-js' },
  { name: 'dashboard css', path: 'apps/dashboard/dist', max: 120 * 1024, kind: 'css' },
  { name: 'pnpm lockfile', path: 'pnpm-lock.yaml', max: 300 * 1024, kind: 'file' },
];
let failures = 0;

function jsFiles(dir) {
  const out = [];
  const walk = (d) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.isFile() && p.endsWith('.js')) out.push(p);
    }
  };
  walk(dir);
  return out;
}

// Sum of entry chunks + chunks imported by more than one module = the JS
// every first page view downloads. Single-importer chunks are lazy routes.
function sharedJsBytes(dir) {
  const files = jsFiles(dir);
  const texts = new Map(files.map((f) => [f, readFileSync(f, 'utf8')]));
  const names = files.map((f) => f.split(/[/\\]/).pop());
  let total = 0;
  for (const f of files) {
    const name = f.split(/[/\\]/).pop();
    const isEntry = f.includes(`${'/'}entry${'/'}`) || f.includes(`${'\\'}entry${'\\'}`);
    let importers = 0;
    for (const [other, text] of texts) {
      if (other !== f && text.includes(name)) importers++;
    }
    if (isEntry || importers > 1) total += statSync(f).size;
  }
  return total;
}

function dirBytes(dir) {
  let total = 0;
  let largest = { name: '', size: 0 };
  let css = 0;
  const walk = (d) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.isFile()) {
        const s = statSync(p).size;
        total += s;
        if (s > largest.size) largest = { name: p, size: s };
        if (p.endsWith('.css')) css += s;
      }
    }
  };
  if (existsSync(dir)) walk(dir);
  return { total, largest, css };
}

for (const b of budgets) {
  const abs = join(ROOT, b.path);
  if (!existsSync(abs)) { console.log(`skip  ${b.name} (missing ${b.path})`); continue; }
  let size = 0, extra = '';
  if (b.kind === 'file') size = statSync(abs).size;
  else if (b.kind === 'shared-js') size = sharedJsBytes(abs);
  else {
    const { total, largest, css } = dirBytes(abs);
    if (b.kind === 'largest') {
      if (b.allow && b.allow.test(largest.name)) { console.log(`skip  ${b.name}: ${largest.name.split('/').pop()} ${(largest.size / 1024).toFixed(0)}KB allowlisted (browser/xterm route)`); continue; }
      size = largest.size; extra = ` (${largest.name.split('/').pop()})`;
    } else if (b.kind === 'css') size = css;
    else size = total;
  }
  const kb = (size / 1024).toFixed(0);
  const maxKb = (b.max / 1024).toFixed(0);
  const ok = size <= b.max;
  if (!ok) failures++;
  console.log(`${ok ? 'pass' : 'FAIL'}  ${b.name}: ${kb}KB / ${maxKb}KB${extra}`);
}

// Duplicate native-binary deps in the lockfile (each ships MBs).
if (existsSync(join(ROOT, 'pnpm-lock.yaml'))) {
  const lock = readFileSync(join(ROOT, 'pnpm-lock.yaml'), 'utf8');
  for (const dep of ['esbuild', '@astrojs/compiler']) {
    const versions = new Set([...lock.matchAll(new RegExp(`  ${dep.replace(/[@/]/g, '\\$&')}@([^:]+):`, 'g'))].map((m) => m[1]));
    console.log(`${versions.size <= 1 ? 'pass' : 'WARN'}  ${dep} versions: ${[...versions].join(', ') || 'n/a'}`);
  }
}
if (STRICT && failures > 0) { console.error(`check-weight: ${failures} budget breach(es)`); process.exit(1); }
console.log('check-weight: done');
