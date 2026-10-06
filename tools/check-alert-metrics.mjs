// Gate: every metric referenced by a Prometheus alert must actually be emitted
// by the gateway.
//
// Why this exists: all five alerts in deploy/helm/ubag/templates/prometheusrule.yaml
// were dead. Three named series that do not exist anywhere in the codebase
// (`http_requests_total`, `ubag_jobs_failed_total`, `ubag_jobs_completed_total`)
// and one used a `_bucket` series the gateway never writes (HTTP duration is
// published as _sum/_count only). The dashboard validator could not catch this
// because its regex only matched `ubag_*` names, and it does not read alert
// files at all - so the entire alerting capability was decorative while looking
// configured.
//
// This reads the metric names the gateway actually writes in handleMetrics and
// fails if an alert references anything else. It is wired into `pnpm check`.

import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

const failures = [];

// ── 1. What the gateway actually emits ────────────────────────────────────────
const GATEWAY_DIR = 'apps/gateway/internal';
const EMITTED = new Set();

function walk(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      walk(full);
      continue;
    }
    if (!full.endsWith('.go') || full.endsWith('_test.go')) continue;
    const text = readFileSync(full, 'utf8');
    // Metric literals appear as "ubag_something" in Fprintf/Print calls,
    // immediately followed by `{labels}` or an end quote. _test.go files are
    // excluded so a fixture cannot make a real gap look covered.
    for (const m of text.matchAll(/"((?:ubag|go|process)_[a-z0-9_]+)[\"{]/g)) {
      EMITTED.add(m[1]);
    }
  }
}
walk(GATEWAY_DIR);

// A metric may be emitted through writeDurationHistogram(name, ...) or a
// runtime-built name, so also accept the base names the histogram helper and
// the *_count/_sum pairs imply.
const BASES = new Set(EMITTED);
for (const name of [...EMITTED]) {
  if (name.endsWith('_count') || name.endsWith('_sum')) {
    BASES.add(name.replace(/_(count|sum)$/, ''));
  }
}
for (const name of [...BASES]) {
  BASES.add(`${name}_bucket`);
}

if (BASES.size < 20) {
  failures.push(
    `only found ${BASES.size} emitted metric names under ${GATEWAY_DIR} - ` +
      'the scan is probably broken, not the gateway'
  );
}

// ── 2. What the alerts reference ─────────────────────────────────────────────
const ALERT_FILES = [
  'deploy/helm/ubag/templates/prometheusrule.yaml',
  'deploy/prometheus/helper-plane-alerts.yaml',
  'deploy/grafana/dashboards'
];

const referenced = new Map(); // metric -> [files]

for (const target of ALERT_FILES) {
  let text;
  try {
    text = readFileSync(target, 'utf8');
  } catch {
    continue; // optional path
  }
  // Strip Helm templating so `{{ ... }}` cannot be mistaken for a metric name,
  // and drop comment lines: prometheusrule.yaml documents the dead metric names
  // it replaced, and those mentions must not be read as live references.
  const cleaned = text
    .replace(/\{\{[^}]*\}\}/g, ' ')
    .split('\n')
    .filter((line) => !/^\s*#/.test(line))
    .join('\n');
  for (const m of cleaned.matchAll(/\b(ubag_[a-z0-9_]+|[a-z][a-z0-9_]*_total|[a-z][a-z0-9_]*_bucket|[a-z][a-z0-9_]*_seconds)\b/g)) {
    const name = m[1];
    if (!referenced.has(name)) referenced.set(name, []);
    referenced.get(name).push(target);
  }
}

// Series every Prometheus install provides; not gateway responsibilities.
const EXTERNAL_OK = new Set([
  'up',
  'scrape_duration_seconds',
  'scrape_samples_scraped',
  'scrape_samples_post_metric_relabeling'
]);

for (const [name, files] of referenced) {
  if (EXTERNAL_OK.has(name)) continue;
  if (BASES.has(name)) continue;
  failures.push(
    `alert/dashboard references metric "${name}" which the gateway never emits ` +
      `(referenced by ${[...new Set(files)].join(', ')})`
  );
}

if (failures.length) {
  console.error(`Alert-metric checks failed:\n${failures.map((f) => `- ${f}`).join('\n')}`);
  process.exit(1);
}

console.log(
  `Alert-metric checks passed: ${referenced.size} referenced series, ` +
    `${BASES.size} emitted by the gateway.`
);
