#!/usr/bin/env node
/**
 * tools/synthetic-monitor/monitor.mjs — Task 2.6
 *
 * Canary runner that periodically submits a known probe job to each configured
 * adapter endpoint, records success/latency, and updates ubag_synthetic_*
 * Prometheus metrics.
 *
 * Usage:
 *   node tools/synthetic-monitor/monitor.mjs [--config config.json] [--once]
 *
 * Config file format (JSON):
 * {
 *   "targets": [
 *     { "name": "gateway-local", "url": "http://localhost:4000", "secret": "dev-secret" }
 *   ],
 *   "interval_seconds": 60,
 *   "timeout_ms": 10000,
 *   "metrics_port": 9091
 * }
 */

import { readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';

// ── SLO / failure budget math ─────────────────────────────────────────────────

class RollingWindow {
  constructor(windowMs) {
    this._windowMs = windowMs;
    this._outcomes = []; // [{ts, success, latencyMs}]
  }

  record(success, latencyMs = 0) {
    const now = Date.now();
    this._outcomes.push({ ts: now, success, latencyMs });
    this._trim(now);
  }

  _trim(now) {
    const cutoff = now - this._windowMs;
    this._outcomes = this._outcomes.filter(o => o.ts >= cutoff);
  }

  stats(targetAvailability = 0.999) {
    const now = Date.now();
    this._trim(now);
    const total = this._outcomes.length;
    const failures = this._outcomes.filter(o => !o.success).length;
    const latencies = this._outcomes.filter(o => o.success).map(o => o.latencyMs).sort((a, b) => a - b);
    const errorRate = total > 0 ? failures / total : 0;
    const allowedErrorRate = 1 - targetAvailability;
    const burnRate = allowedErrorRate > 0 ? errorRate / allowedErrorRate : 0;
    const p50 = latencies[Math.floor(latencies.length * 0.5)] ?? 0;
    const p99 = latencies[Math.floor(latencies.length * 0.99)] ?? 0;
    return { total, failures, errorRate, burnRate, p50, p99, successRate: total > 0 ? 1 - errorRate : 1 };
  }
}

// ── Prometheus metrics ────────────────────────────────────────────────────────

class MetricsStore {
  constructor() {
    this._gauges = new Map();
    this._counters = new Map();
  }

  set(name, labels, value) {
    this._gauges.set(this._key(name, labels), { name, labels, value });
  }

  inc(name, labels, by = 1) {
    const k = this._key(name, labels);
    const existing = this._counters.get(k) || { name, labels, value: 0 };
    existing.value += by;
    this._counters.set(k, existing);
  }

  _key(name, labels) {
    return name + JSON.stringify(labels);
  }

  render() {
    const lines = [];
    for (const { name, labels, value } of [...this._counters.values(), ...this._gauges.values()]) {
      const lbl = Object.entries(labels).map(([k, v]) => `${k}="${v}"`).join(',');
      lines.push(`${name}{${lbl}} ${value}`);
    }
    return lines.join('\n') + '\n';
  }
}

// ── Probe logic ───────────────────────────────────────────────────────────────

async function probe(target, timeoutMs) {
  const start = Date.now();
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const idempotencyKey = `synthetic-probe-${Date.now()}`;
    const body = JSON.stringify({
      job: {
        target: 'synthetic.health.v1',
        command_type: 'probe',
        input: { probe: true, ts: new Date().toISOString() },
      },
      client: { app_id: 'synthetic-monitor', app_version: '1.0.0', sdk: { name: 'synthetic', version: '1.0.0' } },
    });

    const res = await fetch(`${target.url}/v1/jobs`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Idempotency-Key': idempotencyKey,
        'Authorization': `Bearer ${target.secret || 'dev-secret'}`,
        'Ubag-Tenant-Id': 'synthetic',
        'Ubag-App-Id': 'monitor',
      },
      body,
      signal: controller.signal,
    });

    clearTimeout(timer);
    const latencyMs = Date.now() - start;

    // 2xx is success for the probe; 422/429 are expected rejections in CI.
    const success = res.status >= 200 && res.status < 300;
    return { success, latencyMs, statusCode: res.status };
  } catch (err) {
    clearTimeout(timer);
    const latencyMs = Date.now() - start;
    return { success: false, latencyMs, error: err.message };
  }
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  const args = process.argv.slice(2);
  const once = args.includes('--once');
  const configArg = args.indexOf('--config');
  const configPath = configArg >= 0 ? args[configArg + 1] : null;

  let config = {
    targets: [{ name: 'gateway-local', url: 'http://localhost:4000', secret: 'dev-secret' }],
    interval_seconds: 60,
    timeout_ms: 10000,
    metrics_port: 9091,
    slo_target: 0.999,
    window_seconds: 30 * 24 * 3600,
  };

  if (configPath) {
    try {
      config = { ...config, ...JSON.parse(readFileSync(configPath, 'utf8')) };
    } catch (e) {
      process.stderr.write(`Config error: ${e.message}\n`);
      process.exit(1);
    }
  }

  const metrics = new MetricsStore();
  const windows = new Map();
  for (const t of config.targets) {
    windows.set(t.name, new RollingWindow(config.window_seconds * 1000));
  }

  async function runProbes() {
    for (const target of config.targets) {
      const result = await probe(target, config.timeout_ms);
      const w = windows.get(target.name);
      w.record(result.success, result.latencyMs);

      const labels = { target: target.name };
      metrics.inc('ubag_synthetic_probe_total', { ...labels, outcome: result.success ? 'success' : 'failure' });
      metrics.set('ubag_synthetic_probe_latency_ms', labels, result.latencyMs);

      const stats = w.stats(config.slo_target);
      metrics.set('ubag_synthetic_slo_burn_rate', labels, stats.burnRate.toFixed(6));
      metrics.set('ubag_synthetic_slo_error_rate', labels, stats.errorRate.toFixed(6));
      metrics.set('ubag_synthetic_slo_success_rate', labels, stats.successRate.toFixed(6));
      metrics.set('ubag_synthetic_probe_p50_ms', labels, stats.p50);
      metrics.set('ubag_synthetic_probe_p99_ms', labels, stats.p99);
      metrics.set('ubag_synthetic_health', labels, stats.burnRate > 14.4 ? 0 : 1);

      process.stdout.write(
        `[probe] ${target.name} ${result.success ? 'OK' : 'FAIL'} ${result.latencyMs}ms burn=${stats.burnRate.toFixed(2)}\n`
      );
    }
  }

  // Expose metrics over HTTP.
  if (!once) {
    const server = createServer((req, res) => {
      if (req.url === '/metrics' && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'text/plain; version=0.0.4' });
        res.end(metrics.render());
      } else {
        res.writeHead(404);
        res.end();
      }
    });
    server.listen(config.metrics_port, () => {
      process.stdout.write(`Metrics server on :${config.metrics_port}/metrics\n`);
    });
  }

  await runProbes();

  if (!once) {
    setInterval(runProbes, config.interval_seconds * 1000);
  }
}

main().catch(err => {
  process.stderr.write(`Fatal: ${err.message}\n`);
  process.exit(1);
});
