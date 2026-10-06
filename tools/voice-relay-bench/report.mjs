// Byte-compare, paired statistics and report assembly for the voice-relay A/B harness (perf-fleet P7.7). Pure functions.
import { createHash } from 'node:crypto';

export const REPORT_SCHEMA = 'ubag.voice_relay_ab/1';
export const STREAMS = ['control', 'speaker', 'fifo', 'closed', 'second'];
const sha = (text) => createHash('sha256').update(text).digest('hex');
const r3 = (x) => (x == null || !Number.isFinite(x) ? null : Math.round(x * 1000) / 1000);

// ---------------------------------------------------------------- byte compare

const firstDiff = (x, y) => {
  for (let i = 0; i < Math.max(x.length, y.length); i++) if (x[i] !== y[i]) return i;
  return -1;
};

/** Differences between two scenario transcripts on the listed streams ([] = byte-identical). */
export function compareTranscripts(a, b, streams = STREAMS) {
  const diffs = [];
  const add = (stream, index, x, y) => diffs.push({ stream, index, a: x ?? null, b: y ?? null });
  if (streams.includes('control')) {
    const i = firstDiff(a.control, b.control);
    if (i >= 0) add('control', i, a.control[i], b.control[i]);
  }
  for (const key of ['speaker', 'fifo']) {
    if (streams.includes(key) && (a[key].sha256 !== b[key].sha256 || a[key].bytes !== b[key].bytes)) {
      const i = firstDiff(a[key].per, b[key].per);
      add(key, i, a[key].per[i], b[key].per[i]);
    }
  }
  if (streams.includes('closed') && a.closed !== b.closed) add('closed', 0, a.closed, b.closed);
  if (streams.includes('second') && JSON.stringify(a.second ?? null) !== JSON.stringify(b.second ?? null)) add('second', 0, a.second, b.second);
  return diffs;
}

/** Digest of the compared streams of one transcript (what a re-run must reproduce). */
export function transcriptDigest(t, streams = STREAMS) {
  const pick = { control: t.control, speaker: t.speaker.sha256, fifo: t.fifo.sha256, closed: t.closed, second: t.second ?? null };
  return sha(JSON.stringify(Object.fromEntries(streams.map((s) => [s, pick[s]]))));
}

export function compareRuns(scenarios, a, b) {
  const rows = scenarios.map((sc) => {
    const ta = a[sc.id];
    const tb = b[sc.id];
    const streams = sc.compare ?? STREAMS;
    const diffs = ta && tb ? compareTranscripts(ta, tb, streams) : [{ stream: 'missing', index: 0, a: !!ta, b: !!tb }];
    return { id: sc.id, match: diffs.length === 0, a_sha256: ta ? transcriptDigest(ta, streams) : null, b_sha256: tb ? transcriptDigest(tb, streams) : null, diffs };
  });
  // A compare of two empty streams proves nothing: report how much output the identical streams actually carried.
  const all = Object.values(a);
  const coverage = {
    scenarios_with_fifo: all.filter((t) => t.fifo.bytes > 0).length, fifo_bytes: all.reduce((s, t) => s + t.fifo.bytes, 0),
    scenarios_with_speaker: all.filter((t) => t.speaker.frames > 0).length, speaker_frames: all.reduce((s, t) => s + t.speaker.frames, 0),
    scenarios_with_error_reply: all.filter((t) => t.control.some((m) => m.includes('"error"'))).length,
  };
  return {
    passed: rows.every((r) => r.match),
    coverage,
    scenarios: rows.length,
    mismatches: rows.filter((r) => !r.match).length,
    digests: rows.map(({ id, a_sha256, b_sha256 }) => ({ id, a: a_sha256, b: b_sha256 })),
    failures: rows.filter((r) => !r.match).map(({ id, diffs }) => ({ id, diffs })),
  };
}

// ---------------------------------------------------------------- paired statistics

// Two-sided 95% Student t critical values by degrees of freedom.
const T95 = [NaN, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228, 2.201, 2.179, 2.16, 2.145, 2.131, 2.12, 2.11, 2.101, 2.093, 2.086,
  2.08, 2.074, 2.069, 2.064, 2.06, 2.056, 2.052, 2.048, 2.045, 2.042];
export const tCritical = (df) => (df <= 30 ? T95[df] : df <= 60 ? 2.0 : 1.98);

/** Mean and 95% CI of per-pair values; the CI needs at least two pairs. */
export function pairedStats(values) {
  const v = values.filter((x) => Number.isFinite(x));
  const n = v.length;
  if (!n) return { n: 0, mean: null, sd: null, ci_lower: null, ci_upper: null };
  const mean = v.reduce((s, x) => s + x, 0) / n;
  if (n < 2) return { n, mean: r3(mean), sd: null, ci_lower: null, ci_upper: null };
  const sd = Math.sqrt(v.reduce((s, x) => s + (x - mean) ** 2, 0) / (n - 1));
  const half = (tCritical(n - 1) * sd) / Math.sqrt(n);
  return { n, mean: r3(mean), sd: r3(sd), ci_lower: r3(mean - half), ci_upper: r3(mean + half) };
}

/** Percent reduction of B against A ((a-b)/a); positive = the candidate is lower. null when A is not positive. */
export const reductionPct = (a, b) => (Number.isFinite(a) && Number.isFinite(b) && a > 0 ? (100 * (a - b)) / a : null);
const at = (run, path) => path.split('.').reduce((o, k) => (o == null ? null : o[k]), run);
const perPair = (pairs, path, fn = reductionPct) => pairs.map((p) => fn(at(p.a, path), at(p.b, path)));

/** Paired statistics over interleaved A/B runs. Reductions are positive when B is better; regressions are their negation. */
export function summarizePairs(pairs) {
  const reduction = (path) => pairedStats(perPair(pairs, path));
  const regression = (path) => pairedStats(perPair(pairs, path).map((x) => (x == null ? null : -x)));
  const sum = (who, path) => pairs.reduce((s, p) => s + (at(p[who], path) ?? 0), 0);
  return {
    pairs: pairs.length,
    cpu_reduction_pct: reduction('cpu_ms_per_call_minute'),
    rss_reduction_pct: reduction('rss_peak_kib'),
    mic_p95_regression_pct: regression('mic.p95_ms'),
    mic_p99_regression_pct: regression('mic.p99_ms'),
    speaker_p95_regression_pct: regression('speaker.p95_ms'),
    speaker_p99_regression_pct: regression('speaker.p99_ms'),
    mixed_container_cpu_reduction_pct: reduction('mixed.container_cpu_pct'),
    mixed_job_p95_reduction_pct: reduction('mixed.job_p95_ms'),
    // work counters: a drop-in relay moves exactly as many frames as the baseline, so these are 0 %
    mic_blocks_delta_pct: pairedStats(perPair(pairs, 'mic_blocks', (a, b) => (a > 0 && Number.isFinite(b) ? (100 * (b - a)) / a : null))),
    speaker_frames_delta_pct: pairedStats(perPair(pairs, 'speaker_frames', (a, b) => (a > 0 && Number.isFinite(b) ? (100 * (b - a)) / a : null))),
    drops: { a: sum('a', 'mic_drops') + sum('a', 'speaker_drops'), b: sum('b', 'mic_drops') + sum('b', 'speaker_drops') },
  };
}

// ---------------------------------------------------------------- leaks

/** Growth over N reconnects, each metric the minimum of several samples (a transient pactl child would otherwise read as a leak). */
export function leakVerdict(before, after, maxRssGrowthKib) {
  const delta = { fds: after.fds - before.fds, threads: after.threads - before.threads, children: after.children - before.children, rss_kib: after.rss_kib - before.rss_kib };
  const leaks = ['fds', 'threads', 'children'].filter((k) => delta[k] > 0).length + (delta.rss_kib > maxRssGrowthKib ? 1 : 0);
  return { supported: true, before, after, delta, leaks, max_rss_growth_kib: maxRssGrowthKib };
}

// ---------------------------------------------------------------- report

export function buildReport(parts) {
  const { pairs, compare } = parts;
  return {
    schema: REPORT_SCHEMA,
    generated_at: parts.generatedAt ?? new Date().toISOString(),
    self_test: !!parts.selfTest,
    authoritative: !!parts.authoritative,
    non_authoritative_reasons: parts.reasons ?? [],
    host_class: parts.hostClass ?? 'unspecified',
    host: parts.host,
    libopus_version: parts.libopusVersion ?? null,
    codec: parts.codec,
    corpus: parts.corpus,
    impls: parts.impls,
    golden: parts.golden,
    compare,
    run: parts.run,
    pairs,
    summary: summarizePairs(pairs),
    leaks: parts.leaks,
    mixed_probe: !!parts.mixedProbe,
  };
}

export function renderMarkdown(report) {
  const s = report.summary;
  const f = (st) => (st?.mean == null ? 'n/a' : `${st.mean} % (95% CI ${st.ci_lower ?? 'n/a'} .. ${st.ci_upper ?? 'n/a'}, n=${st.n})`);
  const L = [
    `# Voice relay A/B (${report.impls.a.label} vs ${report.impls.b.label})`, '',
    `${report.authoritative ? '' : '**NON-AUTHORITATIVE** (' + report.non_authoritative_reasons.join('; ') + ')'}${report.self_test ? ' **SELF-TEST: both sides are the same Python relay with a fake codec**' : ''}`.trim(), '',
    `libopus: ${report.libopus_version ?? 'unknown'} (${report.codec}); host class: ${report.host_class}; corpus seed ${report.corpus.seed}, sha256 ${report.corpus.sha256}`, '',
    `## Byte compare: ${report.compare.passed ? 'PASS' : 'FAIL'} (${report.compare.scenarios - report.compare.mismatches}/${report.compare.scenarios} scenarios identical)`, '',
    ...report.compare.failures.map((x) => `- ${x.id}: ${x.diffs.map((d) => `${d.stream}@${d.index}`).join(', ')}`), '',
    '## Paired deltas (B against A, interleaved)', '',
    `- CPU per call-minute reduction: ${f(s.cpu_reduction_pct)}`,
    `- peak RSS reduction: ${f(s.rss_reduction_pct)}`,
    `- mic transit p95 / p99 regression: ${f(s.mic_p95_regression_pct)} / ${f(s.mic_p99_regression_pct)}`,
    `- speaker transit p95 / p99 regression: ${f(s.speaker_p95_regression_pct)} / ${f(s.speaker_p99_regression_pct)}`,
    `- frames moved, mic / speaker (must be 0 %): ${f(s.mic_blocks_delta_pct)} / ${f(s.speaker_frames_delta_pct)}`,
    `- drops A / B: ${s.drops.a} / ${s.drops.b}`,
    `- leaks A / B: ${report.leaks?.a?.supported ? `${report.leaks.a.leaks} / ${report.leaks.b.leaks}` : 'not measured'}`, '',
  ];
  return L.join('\n');
}
