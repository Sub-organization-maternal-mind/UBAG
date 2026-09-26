// Gate npm dependency advisories on REGRESSION rather than on absolute count.
//
// `pnpm audit` currently reports 48 advisories (2 critical, 22 high) and none of
// them has a patched version available. Every one is build tooling - vite,
// postcss, browserslist, nanoid, vitest, and the astro/starlight docs toolchain.
// None of it ships: the gateway is a Go binary, and apps/dashboard deploys
// static files with no Node runtime, so a dev-server advisory in vitest or an
// image-optimizer RCE in astro is not reachable in production.
//
// A gate that can never go green is not a gate. It gets `--no-verify`'d, or
// deleted, and then nothing is checked at all - which is exactly the state this
// repo was in (there was no npm audit anywhere). So instead of
// `pnpm audit --audit-level high`, this compares against a committed baseline
// and FAILS on any increase. A new vulnerable dependency, or a new advisory in
// a package we do pull in, breaks the build; the known, unfixable, build-only
// backlog does not block delivery but is tracked in a reviewable file.
//
// Usage:
//   node tools/check-npm-audit.mjs            # check (CI)
//   node tools/check-npm-audit.mjs --update   # rewrite the baseline deliberately

import { readFileSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const BASELINE_PATH = 'tools/audit-baseline.json';
const update = process.argv.includes('--update');

const audit = spawnSync('cmd', ['/c', 'pnpm', 'audit', '--json'], {
  encoding: 'utf8',
  maxBuffer: 64 * 1024 * 1024
});

if (audit.error || audit.status === null) {
  console.error(`check-npm-audit: could not run pnpm audit: ${audit.error?.message ?? 'no result'}`);
  process.exit(1);
}

// pnpm prints the JSON report to stdout, but on a non-zero exit (which is the
// normal case when advisories exist) it can also prepend warnings, so locate the
// first brace rather than trusting the whole stream.
const raw = audit.stdout ?? '';
const start = raw.indexOf('{');
if (start === -1) {
  console.error('check-npm-audit: pnpm audit produced no JSON report');
  console.error(raw.slice(0, 800));
  process.exit(1);
}

let report;
try {
  report = JSON.parse(raw.slice(start));
} catch (err) {
  console.error(`check-npm-audit: could not parse the audit report: ${err.message}`);
  process.exit(1);
}

const advisories = Object.values(report.advisories ?? {});
const SEVERITIES = ['critical', 'high', 'moderate', 'low'];

const counts = { critical: 0, high: 0, moderate: 0, low: 0 };
const byWorkspace = {};

for (const advisory of advisories) {
  const severity = advisory.severity;
  if (!(severity in counts)) continue;
  counts[severity] += 1;

  const finding = (advisory.findings ?? [])[0] ?? {};
  const paths = finding.paths ?? [];
  // pnpm path form: "apps__dashboard>vite>postcss". The first segment is the
  // owning workspace; root-level deps come through as just the package name.
  const owner = (paths[0] ?? '').split('>')[0] || '(root)';
  byWorkspace[owner] ??= { critical: 0, high: 0, moderate: 0, low: 0 };
  byWorkspace[owner][severity] += 1;
}

const measured = {
  _comment:
    'Known npm advisories as of 2026-09-27. tools/check-npm-audit.mjs fails the build if any severity count INCREASES, or if a new critical/high appears in a workspace that currently has none. Every entry here is build tooling (vite, postcss, browserslist, nanoid, vitest, astro/starlight) that is not present in a deployed artifact: the gateway is a Go binary and apps/dashboard ships static files with no Node runtime. pnpm reports no patched version for any of them, so a plain `pnpm audit` gate could never go green and would train everyone to bypass it. Re-run `node tools/check-npm-audit.mjs --update` after an intentional upgrade and commit the new numbers with a note in PROGRESS.md.',
  ...counts,
  byWorkspace
};

if (update) {
  writeFileSync(BASELINE_PATH, `${JSON.stringify(measured, null, 2)}\n`);
  console.log('check-npm-audit: baseline updated');
  for (const s of SEVERITIES) console.log(`  ${s}: ${measured[s]}`);
  process.exit(0);
}

const baseline = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'));
const failures = [];

for (const severity of SEVERITIES) {
  const before = baseline[severity] ?? 0;
  const after = counts[severity];
  if (after > before) {
    failures.push(
      `${severity} advisories increased: ${before} -> ${after}. ` +
        'Either upgrade the dependency, or if the new advisory is genuinely ' +
        'build-only and unfixable, re-run with --update and note why in PROGRESS.md.'
    );
  } else if (after < before) {
    console.log(`  improved: ${severity} ${before} -> ${after}`);
  }
}

for (const [owner, perSeverity] of Object.entries(byWorkspace)) {
  const before = baseline.byWorkspace?.[owner] ?? {};
  for (const severity of SEVERITIES) {
    const b = before[severity] ?? 0;
    const a = perSeverity[severity] ?? 0;
    if (a > b) {
      failures.push(`${owner}: ${severity} advisories increased ${b} -> ${a}`);
    }
  }
}

// Any workspace that is currently clean must stay clean.
for (const [owner, before] of Object.entries(baseline.byWorkspace ?? {})) {
  if (owner in byWorkspace) continue;
  failures.push(
    `${owner} no longer appears in the audit report - update the baseline if that is expected`
  );
}

const total = SEVERITIES.reduce((sum, s) => sum + counts[s], 0);
if (failures.length) {
  console.error(`check-npm-audit: ${total} advisories, and the baseline was exceeded:\n`);
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}

console.log(
  `check-npm-audit: ${total} known advisories, all within the committed baseline ` +
    `(${SEVERITIES.map((s) => `${s}=${counts[s]}`).join(' ')}). No regression.`
);
