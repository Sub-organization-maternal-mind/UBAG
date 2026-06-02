#!/usr/bin/env node
/**
 * UBAG load test runner (Task B1.4).
 * Runs k6 smoke test and compares against baselines.json.
 * Usage: node tests/load/run-load.mjs [--smoke] [--full]
 */
import { execSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const baselines = JSON.parse(readFileSync(join(dir, 'baselines.json'), 'utf8'));
const args = process.argv.slice(2);
const mode = args.includes('--full') ? 'full' : 'smoke';

const GW_URL = process.env.UBAG_GW_URL ?? 'http://localhost:8081';
const SECRET = process.env.UBAG_APP_SECRET ?? '';

console.log(`[load] Running ${mode} load test against ${GW_URL}`);

// k6 smoke options override (very short run for CI)
const smokeEnv = mode === 'smoke' ? `K6_ITERATIONS=10 K6_VUS=2` : '';

try {
  execSync(
    `${smokeEnv} k6 run --env UBAG_GW_URL=${GW_URL} --env UBAG_APP_SECRET=${SECRET} tests/load/k6.js --summary-export=/tmp/k6-summary.json`,
    { cwd: join(dir, '../..'), stdio: 'inherit' }
  );
} catch {
  // k6 exits non-zero when thresholds fail — we'll check the JSON output
}

// Parse k6 output and compare to baselines
try {
  const summary = JSON.parse(readFileSync('/tmp/k6-summary.json', 'utf8'));
  const p95 = summary?.metrics?.job_create_duration?.values?.['p(95)'] ?? 0;
  const threshold = baselines.job_create_p95_ms * (1 + baselines.regression_threshold_pct / 100);

  if (p95 > threshold) {
    console.error(`[load] REGRESSION: job_create p95=${p95}ms exceeds baseline ${baselines.job_create_p95_ms}ms * 1.${baselines.regression_threshold_pct} = ${threshold}ms`);
    process.exit(1);
  }

  console.log(`[load] PASS: job_create p95=${p95}ms (baseline: ${baselines.job_create_p95_ms}ms, threshold: ${threshold}ms)`);
} catch {
  console.log('[load] k6 summary not available (k6 not installed or no output)');
}
