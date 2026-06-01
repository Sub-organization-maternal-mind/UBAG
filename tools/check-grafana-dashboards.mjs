#!/usr/bin/env node
/**
 * check-grafana-dashboards.mjs — Task 2.5
 *
 * Validates all Grafana dashboard JSON files in deploy/grafana/dashboards/:
 *   • Valid JSON
 *   • Has "title" and "uid" fields
 *   • All metric expressions reference only contract metric names (ubag_*)
 *   • No duplicate UIDs
 *
 * Usage:
 *   node tools/check-grafana-dashboards.mjs [dashboards-dir]
 *
 * Exit code 0 = all checks pass, 1 = violations found.
 */

import { readdirSync, readFileSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';

// ── Contract metric names (must match packages/observability/src/metrics.mjs) ─
const CONTRACT_METRIC_NAMES = new Set([
  'ubag_gateway_http_requests_total',
  'ubag_gateway_http_request_duration_seconds',
  'ubag_gateway_http_inflight_requests',
  'ubag_gateway_ready',
  'ubag_jobs_created_total',
  'ubag_jobs_current',
  'ubag_jobs_duration_seconds',
  'ubag_queue_depth',
  'ubag_queue_oldest_job_age_seconds',
  'ubag_worker_jobs_processed_total',
  'ubag_worker_job_duration_seconds',
  'ubag_worker_result_ingestions_total',
  'ubag_worker_result_ingestion_duration_seconds',
  'ubag_adapter_requests_total',
  'ubag_adapter_request_duration_seconds',
  'ubag_webhook_deliveries_total',
  'ubag_webhook_delivery_duration_seconds',
  'ubag_webhook_outbox_depth',
  'ubag_webhook_outbox_oldest_age_seconds',
  'ubag_idempotency_replays_total',
  'ubag_sse_connections_current',
  'ubag_artifact_captures_total',
]);

// Regex to extract metric names from PromQL expressions.
const METRIC_NAME_RE = /\bubag_[a-z][a-z0-9_]*\b/g;

function extractMetricNames(expr) {
  const names = new Set();
  for (const m of (expr || '').matchAll(METRIC_NAME_RE)) {
    // Strip histogram suffixes.
    const base = m[0]
      .replace(/_sum$/, '')
      .replace(/_count$/, '')
      .replace(/_bucket$/, '');
    names.add(base);
  }
  return names;
}

function collectExprs(obj, exprs = []) {
  if (!obj || typeof obj !== 'object') return exprs;
  if (Array.isArray(obj)) {
    for (const item of obj) collectExprs(item, exprs);
  } else {
    if (typeof obj.expr === 'string') exprs.push(obj.expr);
    for (const v of Object.values(obj)) collectExprs(v, exprs);
  }
  return exprs;
}

function validateDashboard(file, content) {
  const failures = [];
  let dashboard;

  try {
    dashboard = JSON.parse(content);
  } catch (e) {
    return [`${file}: invalid JSON — ${e.message}`];
  }

  if (!dashboard.title || typeof dashboard.title !== 'string' || !dashboard.title.trim()) {
    failures.push(`${file}: missing or empty "title" field`);
  }

  if (!dashboard.uid || typeof dashboard.uid !== 'string' || !dashboard.uid.trim()) {
    failures.push(`${file}: missing or empty "uid" field`);
  }

  // Check all PromQL expressions reference only contract metrics.
  const exprs = collectExprs(dashboard);
  for (const expr of exprs) {
    for (const name of extractMetricNames(expr)) {
      if (!CONTRACT_METRIC_NAMES.has(name)) {
        failures.push(`${file}: expression references non-contract metric "${name}": ${expr.slice(0, 80)}`);
      }
    }
  }

  return failures;
}

function main() {
  const dashDir = resolve(
    process.argv[2] || join(import.meta.dirname || process.cwd(), '../deploy/grafana/dashboards')
  );

  if (!existsSync(dashDir)) {
    process.stderr.write(`Dashboard directory not found: ${dashDir}\n`);
    process.exit(1);
  }

  const files = readdirSync(dashDir).filter(f => f.endsWith('.json')).sort();

  if (files.length === 0) {
    process.stderr.write(`No dashboard JSON files found in ${dashDir}\n`);
    process.exit(1);
  }

  const failures = [];
  const seenUids = new Map();

  for (const file of files) {
    const path = join(dashDir, file);
    const content = readFileSync(path, 'utf8');
    const fileFailures = validateDashboard(file, content);
    failures.push(...fileFailures);

    // Check for duplicate UIDs.
    try {
      const d = JSON.parse(content);
      if (d.uid) {
        if (seenUids.has(d.uid)) {
          failures.push(`${file}: duplicate uid "${d.uid}" (also in ${seenUids.get(d.uid)})`);
        } else {
          seenUids.set(d.uid, file);
        }
      }
    } catch {}
  }

  if (failures.length === 0) {
    process.stdout.write(`✓ Grafana dashboard check passed — ${files.length} dashboards validated\n`);
    for (const f of files) {
      process.stdout.write(`  ✓ ${f}\n`);
    }
    process.exit(0);
  } else {
    process.stderr.write(`✗ Grafana dashboard check FAILED — ${failures.length} violation(s):\n`);
    for (const f of failures) {
      process.stderr.write(`  • ${f}\n`);
    }
    process.exit(1);
  }
}

main();
