// Structure gate for deploy/prometheus/helper-plane-alerts.yaml (promtool-free).
// Line-based on purpose: the rule file keeps to a flat subset of YAML. Checks
// each alert has name, expr (balanced brackets and quotes), for, severity and
// summary, that names are unique, and that every runbook anchor exists as a
// heading in docs/perf-fleet/RUNBOOK.md. Metric existence is
// tools/check-alert-metrics.mjs's job.

import { readFileSync } from 'node:fs';

const RULES = 'deploy/prometheus/helper-plane-alerts.yaml';
const RUNBOOK = 'docs/perf-fleet/RUNBOOK.md';
const failures = [];

const anchors = new Set(
  readFileSync(RUNBOOK, 'utf8')
    .split('\n')
    .filter((l) => /^#{1,6}\s/.test(l))
    .map((l) =>
      l.replace(/^#+\s*/, '').trim().toLowerCase().replace(/[^a-z0-9 -]/g, '').replace(/ /g, '-')
    )
);

const lines = readFileSync(RULES, 'utf8').split('\n');
if (!lines.some((l) => l === 'groups:')) failures.push('missing top-level "groups:"');

const alerts = [];
let cur = null;
let group = null;
let section = null;
for (const [i, raw] of lines.entries()) {
  if (/^\s*#/.test(raw) || !raw.trim()) continue;
  const where = `${RULES}:${i + 1}`;
  if (/\t/.test(raw)) failures.push(`${where}: tab indentation`);
  let m;
  if ((m = raw.match(/^  - name: (\S+)$/))) {
    group = { name: m[1], rules: 0 };
    cur = null;
  } else if ((m = raw.match(/^      - alert: (\S+)$/))) {
    if (!group) failures.push(`${where}: alert outside a group`);
    else group.rules++;
    cur = { name: m[1], line: i + 1, labels: {}, annotations: {} };
    alerts.push(cur);
    section = null;
  } else if (cur && (m = raw.match(/^        (expr|for): (.+)$/))) {
    cur[m[1]] = m[2].trim();
  } else if (cur && (m = raw.match(/^        (labels|annotations):$/))) {
    section = m[1];
  } else if (cur && section && (m = raw.match(/^          (\w+): "?(.*?)"?$/))) {
    cur[section][m[1]] = m[2];
  } else if (!/^(groups:|    rules:)$/.test(raw)) {
    failures.push(`${where}: unrecognised line (the file must keep to single-line strings): ${raw.trim().slice(0, 60)}`);
  }
}

function balanced(expr) {
  const stack = [];
  const pairs = { ')': '(', ']': '[', '}': '{' };
  let quote = false;
  for (const ch of expr) {
    if (ch === '"') quote = !quote;
    else if (quote) continue;
    else if ('([{'.includes(ch)) stack.push(ch);
    else if (ch in pairs && stack.pop() !== pairs[ch]) return false;
  }
  return !quote && stack.length === 0;
}

const seen = new Set();
for (const a of alerts) {
  const w = `${RULES}:${a.line} (${a.name})`;
  if (seen.has(a.name)) failures.push(`${w}: duplicate alert name`);
  seen.add(a.name);
  if (!/^UBAG[A-Za-z0-9]+$/.test(a.name)) failures.push(`${w}: alert name must be UBAG<CamelCase>`);
  if (!a.expr) failures.push(`${w}: missing expr`);
  else if (!balanced(a.expr)) failures.push(`${w}: unbalanced brackets or quotes in expr`);
  if (!a.for || !/^\d+[smh]$/.test(a.for)) failures.push(`${w}: missing or invalid "for"`);
  if (!['warning', 'critical'].includes(a.labels.severity)) failures.push(`${w}: severity must be warning|critical`);
  if (!a.annotations.summary) failures.push(`${w}: missing summary`);
  const rb = a.annotations.runbook || '';
  const anchor = rb.startsWith('docs/perf-fleet/RUNBOOK.md#') ? rb.split('#')[1] : null;
  if (!anchor) failures.push(`${w}: runbook must point at docs/perf-fleet/RUNBOOK.md#<anchor>`);
  else if (!anchors.has(anchor)) failures.push(`${w}: runbook anchor "#${anchor}" is not a heading in ${RUNBOOK}`);
}
if (alerts.length === 0) failures.push('no alerts found - the scan is probably broken');
if (group && group.rules === 0) failures.push(`group ${group.name} has no rules`);

if (failures.length) {
  console.error(`Helper alert checks failed:\n${failures.map((f) => `- ${f}`).join('\n')}`);
  process.exit(1);
}
console.log(`Helper alert checks passed: ${alerts.length} alerts, ${anchors.size} runbook headings.`);
