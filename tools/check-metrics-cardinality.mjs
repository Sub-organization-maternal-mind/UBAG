#!/usr/bin/env node
/**
 * check-metrics-cardinality.mjs — Task 2.3
 *
 * Parses a Prometheus text-format metrics snapshot (from /v1/metrics or stdin)
 * and validates every series against the packages/observability contract:
 *   • metric name starts with "ubag_"
 *   • no high-cardinality labels (DISALLOWED_METRIC_LABELS)
 *   • ≤ 6 labels per series
 *   • only "bounded" label values (no raw IDs, UUIDs, etc.)
 *
 * Usage:
 *   node tools/check-metrics-cardinality.mjs [metrics-file]
 *   curl -s http://localhost:4000/v1/metrics | node tools/check-metrics-cardinality.mjs
 *
 * Exit code 0 = all checks pass, 1 = violations found.
 */

import { readFileSync, existsSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { createReadStream } from 'node:fs';
import { Readable } from 'node:stream';

// ── Contract constants (mirrors packages/observability/src/metrics.mjs) ──────

const DISALLOWED_METRIC_LABELS = new Set([
  'id', 'job_id', 'trace_id', 'span_id', 'request_id',
  'tenant_id', 'app_id', 'user_id', 'session_id',
  'email', 'ip', 'url', 'path', 'prompt', 'response',
]);

const MAX_LABELS_PER_SERIES = 6;

// Contract metric names (must all be present in output).
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

// High-cardinality value patterns (e.g. UUIDs, hex IDs).
const HIGH_CARDINALITY_VALUE_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$|^[0-9a-f]{20,}$/i;

// ── Parser ────────────────────────────────────────────────────────────────────

/**
 * Parse a Prometheus text-format line into { metricName, labels, value }.
 * Returns null for comment/empty lines.
 */
function parseLine(line) {
  line = line.trim();
  if (!line || line.startsWith('#')) return null;

  // metric_name{labels} value [timestamp]
  // metric_name value [timestamp]
  const braceOpen = line.indexOf('{');
  if (braceOpen === -1) {
    const parts = line.split(/\s+/);
    return parts.length >= 2 ? { metricName: parts[0], labels: {}, value: parts[1] } : null;
  }

  const braceClose = line.indexOf('}', braceOpen);
  if (braceClose === -1) return null;

  const metricName = line.slice(0, braceOpen);
  const labelsStr = line.slice(braceOpen + 1, braceClose);
  const rest = line.slice(braceClose + 1).trim();
  const value = rest.split(/\s+/)[0];

  const labels = {};
  // Simple label parser: key="value" pairs, comma-separated.
  for (const match of labelsStr.matchAll(/(\w+)="([^"]*)"/g)) {
    labels[match[1]] = match[2];
  }

  return { metricName, labels, value };
}

// ── Validation ────────────────────────────────────────────────────────────────

function validate(lines) {
  const failures = [];
  const seenMetrics = new Set();

  for (const rawLine of lines) {
    const parsed = parseLine(rawLine);
    if (!parsed) continue;

    const { metricName, labels } = parsed;

    // Strip _sum / _count / _bucket histogram suffixes for name checks.
    const baseName = metricName
      .replace(/_sum$/, '')
      .replace(/_count$/, '')
      .replace(/_bucket$/, '');

    seenMetrics.add(baseName);

    // Rule 1: name must start with ubag_.
    if (!baseName.startsWith('ubag_')) {
      failures.push(`metric "${metricName}" does not start with "ubag_"`);
      continue;
    }

    // Rule 2: ≤ MAX_LABELS_PER_SERIES labels.
    const labelKeys = Object.keys(labels);
    if (labelKeys.length > MAX_LABELS_PER_SERIES) {
      failures.push(
        `metric "${metricName}" has ${labelKeys.length} labels (max ${MAX_LABELS_PER_SERIES}): ${labelKeys.join(', ')}`
      );
    }

    // Rule 3: no disallowed high-cardinality label names.
    for (const key of labelKeys) {
      if (DISALLOWED_METRIC_LABELS.has(key)) {
        failures.push(
          `metric "${metricName}" uses disallowed high-cardinality label "${key}"`
        );
      }
    }

    // Rule 4: label values must not look like raw IDs or UUIDs.
    for (const [key, val] of Object.entries(labels)) {
      if (HIGH_CARDINALITY_VALUE_RE.test(val)) {
        failures.push(
          `metric "${metricName}" label "${key}" has high-cardinality value "${val.slice(0, 16)}…"`
        );
      }
    }
  }

  // Rule 5: all contract metric names must be present.
  for (const required of CONTRACT_METRIC_NAMES) {
    if (!seenMetrics.has(required) && !seenMetrics.has(required + '_count') && !seenMetrics.has(required)) {
      failures.push(`contract metric "${required}" is missing from the output`);
    }
  }

  return failures;
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  let input;

  const filePath = process.argv[2];
  if (filePath) {
    if (!existsSync(filePath)) {
      process.stderr.write(`File not found: ${filePath}\n`);
      process.exit(1);
    }
    input = createReadStream(filePath);
  } else if (!process.stdin.isTTY) {
    input = process.stdin;
  } else {
    process.stderr.write('Usage: node check-metrics-cardinality.mjs [metrics-file]\n');
    process.stderr.write('       curl -s http://localhost:4000/v1/metrics | node check-metrics-cardinality.mjs\n');
    process.exit(1);
  }

  const lines = [];
  const rl = createInterface({ input, crlfDelay: Infinity });
  for await (const line of rl) {
    lines.push(line);
  }

  const failures = validate(lines);

  if (failures.length === 0) {
    process.stdout.write(`✓ metrics cardinality check passed (${lines.filter(l => l.trim() && !l.startsWith('#')).length} series checked)\n`);
    process.exit(0);
  } else {
    process.stderr.write(`✗ metrics cardinality check FAILED — ${failures.length} violation(s):\n`);
    for (const f of failures) {
      process.stderr.write(`  • ${f}\n`);
    }
    process.exit(1);
  }
}

main().catch(err => {
  process.stderr.write(`Unexpected error: ${err.message}\n`);
  process.exit(1);
});
