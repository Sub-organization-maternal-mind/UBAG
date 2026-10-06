// Offline tests for tests/load/ladder.mjs (P7.2). Every network case runs against the in-process fake gateway from lib/fake-gateway.mjs on
// 127.0.0.1 with fixture cgroup text: nothing here touches a real host, a container or a provider, and no number is a measurement.
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { after, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';
import { ACK_FLAG, makeRequest, parseProm } from './acceptance.mjs';
import {
  HOST_CLASSES, between, capacityOf, checkHostClass, histogramDelta, ladderThresholds, liveQueueDepth, main, makeRng, parseArgs, pickClass, renderMarkdown, run, voiceForStep,
} from './ladder.mjs';
import { KEY, KEY_B, MB, closeFakes, startFake, v2Fixture } from './lib/fake-gateway.mjs';
import { loadWorkload } from './workloads.mjs';

const here = dirname(fileURLToPath(import.meta.url));
after(closeFakes);

const tmp = () => mkdtempSync(join(tmpdir(), 'ubag-ladder-'));
const FAST = ['--step-seconds', '1', '--cooldown-seconds', '0', '--think-scale', '0', '--poll-interval-ms', '10', '--min-samples', '3', '--max-retries', '3', '--deadline-ms', '20000',
  '--tenant-probe-samples', '3', '--docker-interval-ms', '20', '--out-dir', tmp()];
// Latency goals relaxed (not removed) so a loaded CI box cannot flake the healthy cases; the breach cases use the real goals file.
const relaxedGoals = (() => {
  const file = join(tmp(), 'goals.json');
  writeFileSync(file, JSON.stringify({ ...JSON.parse(readFileSync(join(here, 'thresholds.goals.json'), 'utf8')), max_steady_create_p95_ms: 5000, max_steady_read_p95_ms: 5000 }));
  return file;
})();
const cfgFor = (fake, extra = [], env = {}) => parseArgs([`--${ACK_FLAG}`, ...FAST, ...extra], { ...fake.env, ...env });
const deps = (extra = {}) => ({ sleep: (ms) => new Promise((r) => setTimeout(r, Math.min(ms, 5))), harnessGit: () => ({ sha: '0123456789abcdef', dirty: false }), announce: () => {},
  dockerEnv: async () => ['UBAG_WORKER_DAEMON=true', 'UBAG_SYNTHETIC_PROVIDER=1', 'UBAG_WORKER_STAGE_TIMINGS=1', 'UBAG_VOICE_RELAY_SECRET=must-not-leak', ''].join('\n'), ...extra });
const voiceFile = (p95 = 0.6) => {
  const file = join(tmp(), 'voice.json');
  writeFileSync(file, JSON.stringify({ schema: 'ubag-voice-latency/v1', non_authoritative: true, rows: [1, 5, 10, 20].flatMap((sessions) => [
    { sessions, direction: 'mic', samples: 100, p50_ms: 0.1, p95_ms: 0.3, p99_ms: 0.5 }, { sessions, direction: 'speaker', samples: 100, p50_ms: 0.2, p95_ms: p95, p99_ms: 0.9 }]) }));
  return file;
};
// Gateway 0.5 CPU / 1024 MB + browser 1.0 CPU / 1536 MB = the 2c/4G helper ceiling (1.5 CPU / 2560 MB).
const helperExec = ({ memCurrent = 400 * MB } = {}) => async (c) => (c === 'g'
  ? v2Fixture({ cpuMax: '50000 100000', memMax: String(1024 * MB), current: memCurrent })
  : v2Fixture({ cpuMax: '100000 100000', memMax: String(1536 * MB), current: 300 * MB }));
const HEALTHY = ['--workload', 'mixed', '--steps', '1,2,5', '--goals', relaxedGoals, '--cgroup-containers', 'gateway=g,browser=b', '--host-class', '2c4g', '--isolated-lab-host', '--voice-latency', voiceFile()];
const healthyFake = () => startFake({ acceptAttachments: true, perfMetrics: true });
const failedNames = (step) => step.goals.filter((g) => g.status === 'failed').map((g) => g.name);

// --------------------------------------------------------------- pure logic

describe('workload mix generation', () => {
  it('is deterministic per (seed, salt) and follows the manifest weights', () => {
    const a = makeRng(7, 'step', 5, 0); const b = makeRng(7, 'step', 5, 0); const c = makeRng(7, 'step', 5, 1);
    const seq = (r) => Array.from({ length: 5 }, () => r());
    assert.deepEqual(seq(a), seq(b));
    assert.notDeepEqual(seq(makeRng(7, 'step', 5, 0)), seq(c));
    const { mix } = loadWorkload('mixed'); const rand = makeRng(1, 'mix'); const n = 20_000; const got = {};
    for (let i = 0; i < n; i += 1) { const k = pickClass(mix, rand).kind; got[k] = (got[k] ?? 0) + 1; }
    for (const m of mix) assert.ok(Math.abs(got[m.kind] / n - m.weight / 100) < 0.015, `${m.kind}: ${got[m.kind] / n}`);
    const sizes = Array.from({ length: 200 }, () => between(rand, { min: 64, max: 70 }));
    assert.ok(sizes.every((x) => x >= 64 && x <= 70) && new Set(sizes).size === 7);
  });
});

describe('stage histogram deltas', () => {
  it('turns two scrapes into per-label p50/p95 in ms, skipping empty series and counter resets', () => {
    const h = (stage, n, le05, le1, sum) => [
      `ubag_job_stage_duration_seconds_bucket{stage="${stage}",adapter_family="synthetic_chat",le="0.5"} ${le05}`, `ubag_job_stage_duration_seconds_bucket{stage="${stage}",adapter_family="synthetic_chat",le="1"} ${le1}`,
      `ubag_job_stage_duration_seconds_bucket{stage="${stage}",adapter_family="synthetic_chat",le="+Inf"} ${n}`, `ubag_job_stage_duration_seconds_sum{stage="${stage}",adapter_family="synthetic_chat"} ${sum}`,
      `ubag_job_stage_duration_seconds_count{stage="${stage}",adapter_family="synthetic_chat"} ${n}`];
    const before = parseProm([...h('provider_submit', 10, 10, 10, 2), ...h('first_token', 5, 5, 5, 1), ...h('extraction', 0, 0, 0, 0)].join('\n'));
    const after = parseProm([...h('provider_submit', 30, 20, 30, 12), ...h('first_token', 2, 2, 2, 0.4), ...h('extraction', 0, 0, 0, 0)].join('\n'));
    const rows = histogramDelta(before, after, 'ubag_job_stage_duration_seconds');
    assert.equal(rows.length, 1); // extraction has no observations, first_token went backwards (restart)
    assert.deepEqual(rows[0].labels, { stage: 'provider_submit', adapter_family: 'synthetic_chat' });
    assert.equal(rows[0].count, 20); assert.equal(rows[0].mean_ms, 500);
    assert.equal(rows[0].p50_ms, 500); assert.equal(rows[0].p95_ms, 950); // 10 of 20 observations in le=0.5, the rest interpolated inside (0.5, 1]
    assert.deepEqual(histogramDelta(null, null, 'ubag_job_stage_duration_seconds'), []);
  });

  it('reads the real queue-depth shape without double counting', () => {
    const real = parseProm(['ubag_queue_depth{queue="q",state="queued"} 7', 'ubag_queue_depth{queue="q",state="assigned"} 1', 'ubag_queue_depth{queue="q",state="completed"} 90', 'ubag_queue_depth_total{queue="q"} 98', 'ubag_queue_depth_live{queue="q"} 8'].join('\n'));
    assert.equal(liveQueueDepth(real), 8);
    assert.equal(liveQueueDepth(parseProm(['ubag_queue_depth{queue="q",state="queued"} 7', 'ubag_queue_depth{queue="q",state="assigned"} 1', 'ubag_queue_depth{queue="q",state="completed"} 90'].join('\n'))), 8);
    assert.equal(liveQueueDepth(parseProm('other 1')), undefined);
    assert.equal(liveQueueDepth(null), undefined);
  });
});

describe('voice, host class and capacity rules', () => {
  const rows = (sizes) => sizes.flatMap((sessions) => [{ sessions, direction: 'mic', p95_ms: sessions / 10 }, { sessions, direction: 'speaker', p95_ms: sessions / 5 }]);
  it('takes the bench size at or above N as a ceiling, never interpolates, and has none above the largest', () => {
    const rep = { rows: rows([1, 5, 10, 20]) };
    assert.equal(voiceForStep(rep, 1).bench_sessions, 1);
    assert.equal(voiceForStep(rep, 2).bench_sessions, 5); // N=2 has no bench row: the next larger one bounds it
    assert.equal(voiceForStep(rep, 20).worst_p95_ms, 4);
    assert.equal(voiceForStep(rep, 21), null);
    assert.equal(voiceForStep(null, 1), null);
    assert.match(voiceForStep(rep, 5).source, /not live media/);
  });

  it('verifies the summed container limits against the 2c/4G and 4c/8G ceilings, failing closed otherwise', () => {
    const c = (cpu, mem) => ({ cpu_limit_cores: cpu, memory_limit_mb: mem });
    assert.equal(checkHostClass('2c4g', { gateway: c(0.5, 1024), browser: c(1, 1536) }).verified, true);
    assert.equal(checkHostClass('4c8g', { a: c(1, 1280), b: c(2, 3840) }).verified, true);
    const off = checkHostClass('2c4g', { gateway: c(1, 1300), browser: c(2, 4096) }); // the production budget is not a lab class
    assert.equal(off.verified, false); assert.match(off.reason, /differ/);
    assert.match(checkHostClass('2c4g', { a: c(1.5, null) }).reason, /no CPU or memory limit/);
    assert.match(checkHostClass('2c4g', { a: c(1.5, 2560), b: { skipped: 'x' } }).reason, /could not be read/);
    assert.match(checkHostClass('2c4g', {}).reason, /no container limits/);
    assert.match(checkHostClass(undefined, { a: c(1.5, 2560) }).reason, /no --host-class/);
    assert.deepEqual(HOST_CLASSES, { '2c4g': { cpu_cores: 1.5, memory_mb: 2560 }, '4c8g': { cpu_cores: 3, memory_mb: 5120 } });
  });

  it('reports the longest passing prefix as the capacity', () => {
    const s = (n, passed, nm = []) => ({ n, passed, not_measured: nm });
    assert.deepEqual(capacityOf([s(1, true), s(2, true, ['max_voice_relay_p95_ms']), s(5, false), s(10, true)]),
      { highest_n_meeting_goals: 2, first_failed_step: 5, goals_not_measured_at_highest: ['max_voice_relay_p95_ms'], every_goal_measured_at_highest: false });
    assert.equal(capacityOf([s(1, false), s(2, true)]).highest_n_meeting_goals, null);
    assert.equal(capacityOf([s(1, true), { n: 2, skipped: 'x' }]).highest_n_meeting_goals, 1);
    assert.equal(capacityOf([s(1, true), s(2, true)]).every_goal_measured_at_highest, true);
  });

  it('ships the plan goals, merged with the integrity zeros and the ladder-only keys', () => {
    const t = ladderThresholds({ thresholds: join(here, 'thresholds.json'), goals: join(here, 'thresholds.goals.json'), ladderThresholds: join(here, 'thresholds.ladder.json') });
    assert.equal(t.max_steady_create_p95_ms, 200); assert.equal(t.max_steady_read_p95_ms, 100); assert.equal(t.min_memory_headroom_pct, 20); assert.equal(t.max_oom_kills, 0);
    for (const k of ['max_lost_jobs', 'max_cross_tenant_leaks', 'max_result_mismatches', 'max_duplicate_committed_results', 'max_false_successful_truncations', 'max_overload_rate_pct']) assert.equal(t[k], 0, k);
    assert.equal(t.max_create_p95_ms, undefined); // the 100-client burst limit is not a ladder goal
    const bad = join(tmp(), 'base.json'); writeFileSync(bad, '{"max_lost_jobs":0}');
    assert.throws(() => ladderThresholds({ thresholds: bad, goals: join(here, 'thresholds.goals.json'), ladderThresholds: join(here, 'thresholds.ladder.json') }), /ladder needs "max_/);
  });
});

// ------------------------------------------------------------------ refusals

describe('ladder refuses unsafe or unsupported runs before any load', () => {
  const env = { UBAG_LOAD_BASE_URL: 'http://127.0.0.1:1', UBAG_LOAD_API_KEY: 'k' };
  it('needs the acknowledgement first, then the target, key and a known mode and option', () => {
    assert.throws(() => parseArgs(['--workload', 'mixed'], {}), /--i-understand-this-is-load/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], { UBAG_LOAD_API_KEY: 'k' }), /UBAG_LOAD_BASE_URL/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], { UBAG_LOAD_BASE_URL: 'http://127.0.0.1:1' }), /UBAG_LOAD_API_KEY/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--bogus'], env), /unknown option/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--mode', 'chaos'], env), /--mode/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--step-seconds', '0'], env), /--step-seconds/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--host-class', '8c16g'], env), /--host-class/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], { ...env, UBAG_LOAD_API_KEY_B: 'k' }), /must differ/);
  });

  it('keeps the host guard on: a public or unlisted host is refused', () => {
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], { ...env, UBAG_LOAD_BASE_URL: 'https://prod.example.com' }), /UBAG_LOAD_ALLOWED_HOSTS/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`], { ...env, UBAG_LOAD_BASE_URL: 'https://prod.example.com', UBAG_LOAD_ALLOWED_HOSTS: '*' }), /UBAG_LOAD_ALLOWED_HOSTS/);
    assert.doesNotThrow(() => parseArgs([`--${ACK_FLAG}`], { ...env, UBAG_LOAD_BASE_URL: 'http://10.0.0.5:8080' }));
  });

  it('cannot drive live voice media and says how to get voice frame age instead', () => {
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--workload', 'voice'], env), /live voice media.*--voice-latency/);
  });

  it('never defaults to a live provider: audio-upload and real targets need --allow-live-provider', () => {
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--workload', 'audio-upload'], env), /live provider/);
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--workload', 'text', '--target', 'chatgpt_web'], env), /live provider/);
    assert.equal(parseArgs([`--${ACK_FLAG}`, '--workload', 'text', '--target', 'chatgpt_web', '--allow-live-provider'], env).target, 'chatgpt_web');
    assert.equal(parseArgs([`--${ACK_FLAG}`, '--workload', 'audio-upload', '--target', 'synthetic_chat'], env).target, 'synthetic_chat'); // the local fixture accepts wav
    assert.equal(parseArgs([`--${ACK_FLAG}`], env).target, 'synthetic_chat'); // mixed defaults to the local fixture
  });

  it('validates steps, truncation probes and the voice report up front', () => {
    for (const bad of ['0,1', '5,2', '1,1', '1,x', '1,201', '']) assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--steps', bad], env), /--steps|requires a value/, bad);
    assert.deepEqual(parseArgs([`--${ACK_FLAG}`], env).steps, [1, 2, 5, 10, 20]); // the manifest ladder
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--workload', 'text', '--truncation-probes', '1'], env), /needs --target synthetic_chat/);
    const bad = join(tmp(), 'bad-voice.json'); writeFileSync(bad, '{"schema":"nope"}');
    assert.throws(() => parseArgs([`--${ACK_FLAG}`, '--voice-latency', bad], env), /schema/);
  });
});

// -------------------------------------------------------------- ladder runs

describe('ladder against a healthy fake gateway', () => {
  it('runs every step, measures every goal, names the capacity and never leaks a key', async () => {
    const fake = await healthyFake();
    const report = await run(cfgFor(fake, HEALTHY), deps({ dockerExec: helperExec() }), {});
    assert.deepEqual(report.steps.map((s) => s.n), [1, 2, 5]);
    for (const s of report.steps) {
      assert.equal(s.passed, true, `${s.n}: ${JSON.stringify(s.goals.filter((g) => g.status !== 'passed'))}`);
      assert.deepEqual(s.not_measured, [], `step ${s.n}`);
      assert.ok(s.accepted >= 3 && s.completed === s.accepted, `step ${s.n}: ${s.accepted}/${s.completed}`);
      assert.equal(s.summary.results_verified, s.completed);
      assert.equal(s.summary.lost_jobs + s.summary.unfinished_jobs + s.summary.failed_jobs + s.summary.result_mismatches + s.summary.cross_tenant_leaks, 0);
      assert.equal(s.summary.duplicate_committed_results, 0); assert.equal(s.summary.false_successful_truncations, 0);
      assert.equal(s.overload.rate_pct, 0);
      assert.ok(s.summary.tenant_probe_requests > 0); assert.equal(s.summary.memory_headroom_pct, 60.94); // 400 of 1024 MB
      assert.ok(s.accept_p95_ms > 0 && s.read_p95_ms > 0);
      const stage = s.stages.find((g) => g.stage === 'provider_submit');
      assert.deepEqual([stage.adapter_family, stage.p50_ms, stage.p95_ms], ['synthetic_chat', 150, 240]); // 0.1 s observations inside the (0.05, 0.25] bucket
      assert.equal(s.stages.some((g) => g.adapter_family === 'mock'), false); // the empty default series is ignored
      assert.deepEqual(Object.values(s.issued).reduce((a, b) => a + b, 0), s.accepted + (s.counters.unaccepted_jobs ?? 0));
      assert.equal(s.counters.attachment_violations ?? 0, 0);
      if (s.issued.attachment || s.issued.audio_upload) assert.ok(s.counters.attachments_verified > 0, 'attachments are verified by size and sha256');
    }
    assert.equal(report.steps[0].voice.bench_sessions, 1); assert.equal(report.steps[1].voice.bench_sessions, 5); // N=2 is bounded by the 5-session row
    assert.equal(report.steps[2].issued.text > 0, true);
    assert.deepEqual(report.capacity, { highest_n_meeting_goals: 5, first_failed_step: null, goals_not_measured_at_highest: [], every_goal_measured_at_highest: true });
    assert.equal(report.passed, true);
    assert.equal(report.meta.authoritative, true); assert.equal(report.meta.host_class.verified, true);
    assert.deepEqual(report.meta.provenance.container_limits.browser, { container: 'b', cpu_cores: 1, memory_mb: 1536 });
    assert.equal(report.meta.provenance.gateway.commit, 'deadbeefcafe');
    assert.equal(report.meta.provenance.workload.name, 'mixed');
    const md = renderMarkdown(report);
    assert.match(md, /^# UBAG workload ladder capacity report\n/);
    assert.match(md, /\*\*Highest N meeting every goal: 5\*\*\./);
    assert.match(md, /## Stage times[\s\S]*provider_submit[\s\S]*synthetic_chat/);
    const text = JSON.stringify(report) + md;
    assert.ok(!text.includes(KEY) && !text.includes(KEY_B), 'API keys must never reach a report');
    assert.deepEqual(report.meta.provenance.stack_env.gateway, { UBAG_SYNTHETIC_PROVIDER: '1', UBAG_WORKER_DAEMON: 'true', UBAG_WORKER_STAGE_TIMINGS: '1' }); // the flags that were on; no secret
    assert.ok(!text.includes('must-not-leak'));
  });

  it('is NON-AUTHORITATIVE, in the first line, without the lab-host attestation or a verified host class', async () => {
    const fake = await healthyFake();
    const noAttest = await run(cfgFor(fake, HEALTHY.filter((a) => a !== '--isolated-lab-host').concat(['--steps', '1'])), deps({ dockerExec: helperExec() }), {});
    assert.equal(noAttest.meta.authoritative, false);
    assert.match(noAttest.meta.non_authoritative_reasons.join(), /no --isolated-lab-host attestation/);
    assert.match(renderMarkdown(noAttest), /^# UBAG workload ladder capacity report \(NON-AUTHORITATIVE\)\n\n\*\*NON-AUTHORITATIVE\.\*\*/);
    const wrongClass = await run(cfgFor(await healthyFake(), HEALTHY.concat(['--steps', '1'])), deps({ dockerExec: async () => v2Fixture({ cpuMax: '100000 100000', memMax: String(4096 * MB) }) }), {});
    assert.equal(wrongClass.meta.authoritative, false); assert.match(wrongClass.meta.non_authoritative_reasons.join(), /host class not verified/);
    const noCgroup = await run(cfgFor(await healthyFake(), ['--workload', 'mixed', '--steps', '1', '--goals', relaxedGoals, '--isolated-lab-host']), deps(), {});
    assert.equal(noCgroup.meta.authoritative, false);
  });
});

describe('ladder goals fail closed', () => {
  const runWith = async (fakeOpts, extra = [], d = {}) => run(cfgFor(await startFake({ acceptAttachments: true, perfMetrics: true, ...fakeOpts }), [...HEALTHY, ...extra]), deps({ dockerExec: helperExec(), ...d }), {});

  it('a step that needs admission refusals did not sustain N clients: it fails, caps the capacity and stops the ladder', async () => {
    const report = await runWith({ maxActiveJobs: 2 }, ['--steps', '1,5,10']);
    assert.equal(report.steps[0].passed, true);
    assert.equal(report.steps[1].passed, false);
    assert.ok(report.steps[1].overload.rate_pct > 0 && report.steps[1].overload.rejected > 0);
    assert.ok(failedNames(report.steps[1]).includes('max_overload_rate_pct'));
    assert.match(report.steps[2].skipped, /earlier step failed/);
    assert.equal(report.capacity.highest_n_meeting_goals, 1); assert.equal(report.capacity.first_failed_step, 5);
    assert.equal(report.passed, false);
    const all = await runWith({ maxActiveJobs: 2 }, ['--steps', '1,5,10', '--continue-after-fail']);
    assert.equal(all.steps[2].skipped, undefined); // ran anyway, the capacity still is the passing prefix
    assert.equal(all.capacity.highest_n_meeting_goals, 1);
  });

  it('breaches of the real accept p95 (200 ms) and read p95 (100 ms) goals fail the step', async () => {
    const goals = join(here, 'thresholds.goals.json');
    const slowRead = await runWith({ slowGetMs: 150 }, ['--steps', '1', '--goals', goals]);
    assert.ok(slowRead.steps[0].read_p95_ms >= 140); assert.ok(failedNames(slowRead.steps[0]).includes('max_steady_read_p95_ms'));
    const slowCreate = await runWith({ slowCreateMs: 260 }, ['--steps', '1', '--goals', goals]);
    assert.ok(slowCreate.steps[0].accept_p95_ms >= 250); assert.ok(failedNames(slowCreate.steps[0]).includes('max_steady_create_p95_ms'));
    assert.equal(slowCreate.capacity.highest_n_meeting_goals, null);
  });

  it('too few accepted jobs leave the latency goals unmeasured instead of reporting a lucky p95', async () => {
    const report = await runWith({}, ['--steps', '1', '--min-samples', '1000', '--step-seconds', '1']);
    assert.equal(report.steps[0].accept_p95_ms, null);
    assert.ok(report.steps[0].not_measured.includes('max_steady_create_p95_ms') && report.steps[0].not_measured.includes('max_steady_read_p95_ms'));
  });

  it('memory headroom under 20 % fails the step', async () => {
    const report = await runWith({}, ['--steps', '1'], { dockerExec: helperExec({ memCurrent: 900 * MB }) });
    assert.equal(report.steps[0].summary.memory_headroom_pct, 12.11); // 900 of 1024 MB
    assert.deepEqual(failedNames(report.steps[0]), ['min_memory_headroom_pct']);
  });

  it('--require-goals turns every unmeasured goal into a FAIL; without it they are only listed', async () => {
    const lenient = await run(cfgFor(await healthyFake(), ['--workload', 'mixed', '--steps', '1', '--goals', relaxedGoals]), deps(), {});
    assert.equal(lenient.steps[0].passed, true);
    assert.deepEqual(lenient.steps[0].not_measured.sort(), ['max_oom_kills', 'max_voice_relay_p95_ms', 'min_memory_headroom_pct']);
    assert.equal(lenient.capacity.every_goal_measured_at_highest, false);
    assert.match(renderMarkdown(lenient), /UNVERIFIED: not measured at that step/);
    const strict = await run(cfgFor(await healthyFake(), ['--workload', 'mixed', '--steps', '1', '--goals', relaxedGoals, '--require-goals']), deps(), {});
    assert.equal(strict.steps[0].passed, false);
    assert.deepEqual(strict.steps[0].goals.filter((g) => g.status === 'not_measured').map((g) => g.name).sort(), ['max_oom_kills', 'max_voice_relay_p95_ms', 'min_memory_headroom_pct']);
    assert.equal(strict.capacity.highest_n_meeting_goals, null);
    const strictOk = await run(cfgFor(await healthyFake(), [...HEALTHY, '--steps', '1', '--require-goals']), deps({ dockerExec: helperExec() }), {});
    assert.equal(strictOk.passed, true, JSON.stringify(strictOk.steps[0].goals.filter((g) => g.status !== 'passed')));
  });

  it('without the second tenant the isolation goal FAILS, never a vacuous pass', async () => {
    const report = await run(cfgFor(await healthyFake(), [...HEALTHY, '--steps', '1', '--require-goals'], { UBAG_LOAD_API_KEY_B: '' }), deps({ dockerExec: helperExec() }), {});
    assert.equal(report.steps[0].passed, false); // 0 probes is a measured zero: tenant isolation was never checked
    assert.deepEqual(failedNames(report.steps[0]), ['min_tenant_probe_requests']);
  });

  for (const [name, fakeOpts, counter, goal] of [
    ['a truncated result body reported as completed', { resultBug: 'truncated' }, 'false_successful_truncations', 'max_false_successful_truncations'],
    ['a duplicated result body', { resultBug: 'duplicated' }, 'duplicate_committed_results', 'max_duplicate_committed_results'],
    ['a duplicated terminal event', { dupTerminal: true }, 'duplicate_committed_results', 'max_duplicate_committed_results'],
    ['a cross-tenant read', { leak: true }, 'cross_tenant_leaks', 'max_cross_tenant_leaks'],
    ['a wrong result body', { resultBug: 'wrong' }, 'result_mismatches', 'max_result_mismatches'],
    ['completed_with_warnings', { finalStatus: 'completed_with_warnings' }, 'warning_jobs', 'max_warning_jobs'],
  ]) {
    it(`${name} is an integrity FAIL (${goal})`, async () => {
      const report = await runWith(fakeOpts, ['--steps', '1']);
      assert.ok(report.steps[0].summary[counter] > 0, `${counter}=${report.steps[0].summary[counter]}`);
      assert.ok(failedNames(report.steps[0]).includes(goal), JSON.stringify(failedNames(report.steps[0])));
      assert.equal(report.passed, false);
    });
  }

  it('a target that rejects attachments is a FAIL, never a silent skip', async () => {
    const report = await runWith({ acceptAttachments: false }, ['--steps', '1']);
    assert.ok(report.steps[0].summary.attachment_violations > 0);
    assert.ok(failedNames(report.steps[0]).includes('max_attachment_violations'));
  });

  it('a gateway that refuses every create does not become a hot loop: the client stops and the step fails', async () => {
    const report = await runWith({ rejectFirstCreates: 100_000 }, ['--steps', '1', '--max-retries', '0', '--workload', 'text']) /* the fake only refuses JSON creates */;
    assert.ok(report.steps[0].summary.scenario_errors >= 1);
    assert.equal(report.steps[0].accepted, 0);
    assert.equal(report.passed, false);
  });

  it('deadline-cut streams: probes that end completed are false successes, probes that end timed_out are not', async () => {
    const probe = ['--steps', '1', '--truncation-probes', '2', '--workload', 'mixed'];
    const hazard = await runWith({ truncatedStatus: 'completed' }, probe);
    assert.equal(hazard.steps[0].counters.false_successful_truncations, 2);
    assert.ok(failedNames(hazard.steps[0]).includes('max_false_successful_truncations'));
    const strict = await runWith({ truncatedStatus: 'timed_out' }, probe);
    assert.equal(strict.steps[0].counters.truncation_probes_ok, 2);
    assert.equal(strict.steps[0].summary.false_successful_truncations, 0);
    assert.equal(strict.steps[0].passed, true);
  });
});

describe('ladder across workloads', () => {
  const quick = ['--steps', '1', '--goals', relaxedGoals];
  it('text and attachment run on the mock target; the attachment class uses the artifact PUT path', async () => {
    const text = await run(cfgFor(await startFake(), ['--workload', 'text', ...quick]), deps(), {});
    assert.equal(text.steps[0].passed, true); assert.deepEqual(Object.keys(text.steps[0].issued), ['text']);
    const fake = await startFake();
    const att = await run(cfgFor(fake, ['--workload', 'attachment', '--steps', '2', '--goals', relaxedGoals, '--seed', '3']), deps(), {});
    assert.equal(att.steps[0].summary.server_errors_non_overload, 0);
    assert.ok(att.steps[0].issued.attachment > 0, JSON.stringify(att.steps[0].issued));
    assert.ok(att.steps[0].latency_ms.create_attachment.count > 0);
    assert.equal(att.steps[0].summary.attachment_violations, 0); // the PUT outcome is tolerated (4xx), as in the acceptance harness' --with-upload path
  });

  it('audio-upload on the local fixture sends the synthetic WAV and verifies size and sha256', async () => {
    const fake = await startFake({ acceptAttachments: true });
    const report = await run(cfgFor(fake, ['--workload', 'audio-upload', '--target', 'synthetic_chat', ...quick]), deps(), {});
    assert.equal(report.steps[0].passed, true, JSON.stringify(report.steps[0].failed));
    assert.ok(report.steps[0].counters.attachments_verified > 0);
    assert.ok(report.steps[0].latency_ms.create_audio.count > 0);
    assert.ok(fake.st.uploads.length > 0 && fake.st.uploads.every((n) => [160044, 3840044, 24960044].includes(n))); // the three synthetic-wav profiles, nothing else
  });
});

// ------------------------------------------------------------------ paused

describe('paused queue mode (100 clients, 1000 queued, consumer paused)', () => {
  const PAUSED = ['--mode', 'paused', '--clients', '8', '--jobs', '40', '--hold-seconds', '1', '--read-rate', '40', '--goals', relaxedGoals];

  it('holds the jobs queued, times reads against the deep queue and marks the unverified drain as not measured', async () => {
    const fake = await startFake({ paused: true, perfMetrics: true });
    const report = await run(cfgFor(fake, PAUSED), deps(), {});
    const p = report.paused;
    assert.equal(p.accepted, 40); assert.equal(p.queued_at_sweep, 40); assert.equal(p.queue_depth_observed, 40);
    assert.equal(p.drain.attempted, false); assert.ok(p.read_p95_ms > 0 && p.accept_p95_ms > 0);
    assert.equal(p.summary.consumer_progressed, 0);
    assert.equal(report.passed, true, JSON.stringify(p.goals.filter((g) => g.status === 'failed')));
    assert.ok(p.not_measured.includes('min_results_verified') && p.not_measured.includes('max_unfinished_jobs')); // nothing executed, nothing verified
    assert.equal(report.capacity, null);
    assert.match(renderMarkdown(report), /8 clients enqueued 40\/40 jobs[\s\S]*No drain was run/);
    const strict = await run(cfgFor(await startFake({ paused: true, perfMetrics: true }), [...PAUSED, '--require-goals']), deps(), {});
    assert.equal(strict.passed, false); // strict needs the drain (and cgroups): unmeasured goals fail
  });

  it('uses the 100-client burst limit for accept latency, not the steady-state goal', async () => {
    const fake = await startFake({ paused: true, perfMetrics: true, slowCreateMs: 300 });
    const report = await run(cfgFor(fake, PAUSED), deps(), {});
    assert.ok(report.paused.accept_p95_ms >= 300);
    assert.equal(report.paused.goals.find((g) => g.name === 'max_create_p95_ms').limit, 2000);
    assert.equal(report.paused.goals.some((g) => g.name === 'max_steady_create_p95_ms'), false);
    assert.equal(report.paused.failed.includes('max_create_p95_ms'), false);
  });

  it('waits for the operator to resume the consumer, then drains and verifies every acked job', async () => {
    const fake = await startFake({ paused: true, perfMetrics: true });
    const announced = [];
    const report = await run(cfgFor(fake, [...PAUSED, '--resume-wait-seconds', '10']), deps({ announce: (m) => { announced.push(m); fake.st.paused = false; } }), {});
    const p = report.paused;
    assert.match(announced[0], /40 jobs are queued\. Resume the consumer now/);
    assert.equal(p.drain.attempted, true); assert.equal(p.drain.final.completed, 40);
    assert.equal(p.summary.results_verified, 40); assert.ok(p.summary.tenant_probe_requests > 0);
    assert.equal(p.summary.lost_jobs, 0); assert.equal(p.summary.unfinished_jobs, 0);
    assert.equal(report.passed, true, JSON.stringify(p.goals.filter((g) => g.status === 'failed')));
  });

  it('a consumer that never resumes is a FAIL', async () => {
    const fake = await startFake({ paused: true, perfMetrics: true });
    const report = await run(cfgFor(fake, [...PAUSED, '--resume-wait-seconds', '1']), deps(), {});
    assert.ok(report.paused.summary.consumer_not_resumed > 0);
    assert.ok(report.paused.failed.includes('max_consumer_not_resumed'));
    assert.equal(report.passed, false);
  });

  it('a consumer that is not paused is a FAIL: jobs must not leave the queue', async () => {
    const fake = await startFake({ perfMetrics: true }); // consumer running
    const report = await run(cfgFor(fake, [...PAUSED, '--hold-seconds', '2', '--read-rate', '60']), deps(), {});
    assert.ok(report.paused.summary.consumer_progressed > 0);
    assert.ok(report.paused.failed.includes('max_consumer_progressed'));
    assert.ok(report.paused.failed.includes('min_queued_jobs'));
  });

  it('acked jobs that vanish while paused are lost jobs', async () => {
    const fake = await startFake({ paused: true, perfMetrics: true });
    const base = makeRequest(cfgFor(fake, PAUSED));
    const request = async (method, path, o) => { const res = await base(method, path, o); return method === 'GET' && path === '/v1/jobs/job_1' ? { ...res, status: 404, json: undefined } : res; };
    const report = await run(cfgFor(fake, PAUSED), deps({ request }), {});
    assert.equal(report.paused.summary.lost_jobs, 1); assert.ok(report.paused.failed.includes('max_lost_jobs'));
  });
});

// ---------------------------------------------------------------------- CLI

describe('ladder CLI', () => {
  it('writes report.json and summary.md, exits 0 on pass, 1 on a failed goal and 2 on a refusal', async () => {
    const out = tmp(); const fake = await healthyFake(); const silent = console.log; console.log = () => {};
    try {
      const args = [`--${ACK_FLAG}`, ...FAST.slice(0, -2), '--out-dir', out, '--workload', 'text', '--steps', '1', '--goals', relaxedGoals];
      assert.equal(await main(args, fake.env, deps()), 0);
      const bad = await startFake({ resultBug: 'wrong' });
      assert.equal(await main(args, bad.env, deps()), 1);
      assert.equal(await main(['--workload', 'text'], fake.env, deps()), 2); // no acknowledgement
    } finally { console.log = silent; }
    const dirs = (await import('node:fs')).readdirSync(out);
    assert.equal(dirs.length, 2);
    for (const d of dirs) assert.ok(existsSync(join(out, d, 'report.json')) && existsSync(join(out, d, 'summary.md')));
    assert.ok(!readFileSync(join(out, dirs[0], 'report.json'), 'utf8').includes(KEY));
  });
});
