import assert from 'node:assert/strict';
import test from 'node:test';
import { budgetFromBaseline, buildPlan, classify, headroomBudget, renderMarkdown, runMatrix, stepRow } from '../../tools/perf/baseline-matrix.mjs';

const env = { UBAG_LOAD_BASE_URL: 'http://127.0.0.1:8080', UBAG_LOAD_API_KEY: 'x' };

test('plan covers ladder, 100 clients, 1000 queued', () => {
  const names = buildPlan().map((s) => s.name);
  assert.deepEqual(names, ['ladder-1', 'ladder-2', 'ladder-5', 'ladder-10', 'ladder-20', 'clients-100', 'queue-1000', 'events-latency', 'steady-state']);
});

test('headroom budget: 20% rule, limiting term, fail closed', () => {
  // limit 1000, peak 500 -> 800-500=300 free; slot 100 -> 3 extra; host: 2000 avail - 0.2*8000 = 400 -> 4
  const b = headroomBudget({ limitMib: 1000, peakMib: 500, slotCostMib: 100, currentSlots: 1, hostTotalMib: 8000, hostAvailableMib: 2000 });
  assert.equal(b.pool_cap, 4);
  assert.equal(b.limiting_term, 'cgroup');
  assert.equal(headroomBudget({ limitMib: 1000, peakMib: 500, slotCostMib: 100, hostTotalMib: 8000, hostAvailableMib: 1500 }).pool_cap, 1); // host has no room
  assert.equal(headroomBudget({ limitMib: 1000, peakMib: 900, slotCostMib: 100 }).pool_cap, 1); // already over budget
  assert.equal(headroomBudget({ limitMib: null, peakMib: 1, slotCostMib: 1 }).pool_cap, null); // unlimited container
  assert.equal(headroomBudget({ limitMib: 1000, peakMib: 1, slotCostMib: 0 }).pool_cap, null);
});

test('stepRow + budgetFromBaseline read the harness report shape', () => {
  const row = stepRow('queue-1000', {
    summary: { failed_jobs: 0, lost_jobs: 0 },
    thresholds: { passed: true },
    ops: { 'queue-1000/create': { ok: { p95: 79.5 } }, 'queue-1000/completion': { ok: { p95: 900 } } },
    resources: { containers: { gateway: { memory_peak_mb: 600, memory_limit_mb: 1300, memory_headroom_pct: 53.8, cpu_throttled_pct: 1, oom_kills: 0 } } },
  });
  assert.equal(row.create_ok_p95_ms, 79.5);
  assert.equal(row.completion_ok_p95_ms, 900);
  const baseline = { classification: 'x', rows: [row] };
  assert.equal(budgetFromBaseline(baseline, { role: 'gateway', slotCostMib: 100, currentSlots: 1, headroomPct: 20 }).pool_cap, 1 + Math.floor((1040 - 600) / 100));
  assert.equal(budgetFromBaseline(baseline, { role: 'browser', slotCostMib: 100, currentSlots: 1, headroomPct: 20 }).pool_cap, null);
  assert.match(renderMarkdown({ classification: 'c', generated_at: 't', rows: [row] }), /queue-1000 \| true \| 79.5/);
});

test('run refuses without ack and non-private hosts; strips allowed-hosts; labels authority', () => {
  assert.throws(() => runMatrix({}, env), /i-understand-this-is-load/);
  assert.throws(() => runMatrix({ 'i-understand-this-is-load': true }, { ...env, UBAG_LOAD_BASE_URL: 'https://vps.example.com', UBAG_LOAD_ALLOWED_HOSTS: 'vps.example.com' }), /never the shared VPS/);
  let spawned = false;
  const { baseline } = runMatrix({ 'i-understand-this-is-load': true, 'dry-run': true, steps: '1' }, env, () => { spawned = true; });
  assert.equal(spawned, false);
  assert.equal(baseline.rows.length, 5);
  assert.match(classify({ platform: 'win32', isolatedLabHost: true }), /NON-AUTHORITATIVE/);
  assert.match(classify({ platform: 'linux', isolatedLabHost: false }), /NON-AUTHORITATIVE/);
  assert.match(classify({ platform: 'linux', isolatedLabHost: true }), /lab-host/);
  const childEnvs = [];
  const { outDir } = runMatrix({ 'i-understand-this-is-load': true, steps: '1', 'out-dir': process.env.TEMP ?? '.' }, { ...env, UBAG_LOAD_ALLOWED_HOSTS: 'x' }, (_n, _a, o) => { childEnvs.push(o.env); return { status: 2, stdout: '' }; });
  assert.ok(outDir);
  assert.ok(childEnvs.length > 0 && childEnvs.every((e) => !('UBAG_LOAD_ALLOWED_HOSTS' in e)));
});
