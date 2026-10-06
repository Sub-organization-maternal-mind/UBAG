import { beforeEach, describe, expect, it, vi } from 'vitest';
import { UBAG_ENDPOINTS } from '../../../../../packages/sdk-typescript/src/generated/contract-manifest';

const get = vi.fn();
vi.mock('./client', () => ({ api: { get: (...a: unknown[]) => get(...a) } }));

import { snapshots } from '../stores/snapshot';
import {
  EM_DASH,
  barColor,
  capacityPct,
  fmtBytes,
  fmtCount,
  fmtCpu,
  fmtTime,
  humanize,
  loadFleetNodes,
  loadFleetSummary,
  parseFleetNodes,
  parseFleetSummary,
  queueReasonRows,
  readinessLines,
} from './fleet';

const node = (over: Record<string, unknown> = {}) => ({
  node_id: 'helper-1',
  label: 'helper-1',
  region: 'eu-west',
  state: 'eligible',
  ineligible_reason: null,
  heartbeat_at: '2026-10-06T10:00:00Z',
  grant: {
    generation: 4,
    state: 'active',
    reservation_state: 'known',
    valid_until: '2026-10-06T22:00:00Z',
    max_browser_workloads: 3,
    cpu_millis: 1500,
    memory_bytes: 2.5 * 2 ** 30,
    voice_capable: false,
  },
  usage: { workloads_in_use: 1, admission_limit: 2 },
  pressure: { admission_reduced: false, recover_at: null },
  readiness: [],
  ...over,
});

const summary = {
  api_version: '2026-05-22',
  kind: 'fleet_summary',
  nodes_total: 3,
  nodes_by_state: { eligible: 2, ineligible: 0, draining: 1, lost: 0, unknown_reservation: 0 },
  workload_limit_total: 5,
  workloads_in_use_total: 2,
  nodes_pressure_reduced: 1,
  held_by_reason: { identity_busy: 2, no_capacity: 1 },
  trace_id: 't',
};

describe('queue reasons', () => {
  it('humanizes identifier tokens and leaves unknown ones readable', () => {
    expect(humanize('waiting_for_worker')).toBe('Waiting for worker');
    expect(humanize('some_new_hold')).toBe('Some new hold');
  });

  it('keeps non-zero counts, largest first, and tolerates reasons it has no hint for', () => {
    const rows = queueReasonRows({
      waiting_for_worker: 1,
      waiting_for_identity: 4,
      waiting_for_capacity: 0,
      brand_new_reason: 4,
      bad: 'x' as unknown as number,
    });
    expect(rows.map((r) => [r.reason, r.count])).toEqual([
      ['brand_new_reason', 4],
      ['waiting_for_identity', 4],
      ['waiting_for_worker', 1],
    ]);
    expect(rows[1].hint).toMatch(/one operation runs per identity/);
    expect(rows[0]).toMatchObject({ label: 'Brand new reason', hint: '' });
  });

  it('is empty for missing input', () => {
    expect(queueReasonRows(null)).toEqual([]);
    expect(queueReasonRows(undefined)).toEqual([]);
  });
});

describe('formatting: unreported values are an em dash', () => {
  it('counts, times, cpu and memory', () => {
    expect(fmtCount(1200)).toBe((1200).toLocaleString());
    expect(fmtCount(undefined)).toBe(EM_DASH);
    expect(fmtCount('3')).toBe(EM_DASH);
    expect(fmtTime(null)).toBe(EM_DASH);
    expect(fmtTime('')).toBe(EM_DASH);
    expect(fmtTime('not a date')).toBe(EM_DASH);
    expect(fmtTime('2026-10-06T10:00:00Z')).not.toBe(EM_DASH);
    expect(fmtCpu(1500)).toBe('1.5 CPU');
    expect(fmtCpu(1000)).toBe('1 CPU');
    expect(fmtCpu(null)).toBe(EM_DASH);
    expect(fmtBytes(2.5 * 2 ** 30)).toBe('2.5 GiB');
    expect(fmtBytes(512 * 2 ** 20)).toBe('512 MiB');
    expect(fmtBytes(undefined)).toBe(EM_DASH);
  });

  it('capacity percent clamps and never divides by zero; bar turns warning then danger', () => {
    expect(capacityPct(1, 2)).toBe(50);
    expect(capacityPct(5, 2)).toBe(100);
    expect(capacityPct(1, 0)).toBe(0);
    expect(barColor(69)).toBe('bg-success');
    expect(barColor(70)).toBe('bg-warning');
    expect(barColor(90)).toBe('bg-danger');
  });
});

describe('parseFleetSummary', () => {
  it('maps the contract shape', () => {
    expect(parseFleetSummary(summary)).toEqual({
      nodes_total: 3,
      nodes_by_state: summary.nodes_by_state,
      workload_limit_total: 5,
      workloads_in_use_total: 2,
      nodes_pressure_reduced: 1,
      held_by_reason: { identity_busy: 2, no_capacity: 1 },
    });
  });

  it('rejects anything else so the panel stays hidden', () => {
    expect(parseFleetSummary(null)).toBeNull();
    expect(parseFleetSummary({ error: 'not implemented' })).toBeNull();
    expect(parseFleetSummary({ ...summary, nodes_total: '3' })).toBeNull();
  });

  it('drops non-numeric held counts and tolerates a missing held_by_reason', () => {
    const { held_by_reason, ...rest } = summary;
    void held_by_reason;
    expect(parseFleetSummary(rest)?.held_by_reason).toEqual({});
    expect(parseFleetSummary({ ...summary, held_by_reason: { a: 1, b: 'x' } })?.held_by_reason).toEqual({ a: 1 });
  });
});

describe('parseFleetNodes', () => {
  it('maps a node and defaults the optional parts', () => {
    const [n] = parseFleetNodes({ data: [node({ label: '', readiness: undefined, pressure: undefined })] })!;
    expect(n.label).toBe('helper-1');
    expect(n.readiness).toEqual([]);
    expect(n.pressure).toEqual({});
    expect(n.usage).toEqual({ workloads_in_use: 1, admission_limit: 2 });
  });

  it('is null when the body has no node list', () => {
    expect(parseFleetNodes(null)).toBeNull();
    expect(parseFleetNodes({ data: 'x' })).toBeNull();
    expect(parseFleetNodes({ error: 'nope' })).toBeNull();
  });

  it('skips nodes the panels cannot draw', () => {
    const out = parseFleetNodes({
      data: [
        node(),
        node({ node_id: '' }),
        node({ state: undefined }),
        node({ usage: { workloads_in_use: 1 } }),
        node({ grant: undefined }),
        null,
        'x',
      ],
    })!;
    expect(out.map((n) => n.node_id)).toEqual(['helper-1']);
  });

  it('renders readiness as target, state and count', () => {
    const [n] = parseFleetNodes({
      data: [
        node({
          readiness: [
            { target: 'chatgpt_web', session_state: 'authenticated', count: 2, checked_at: 'x' },
            { target: 'gemini_web', session_state: 'login_required', count: 1, checked_at: 'x' },
            { target: 7 },
            null,
          ],
        }),
      ],
    })!;
    expect(readinessLines(n)).toEqual(['chatgpt_web: authenticated ×2', 'gemini_web: login_required ×1']);
  });
});

describe('fleet loaders', () => {
  beforeEach(() => {
    get.mockReset();
    snapshots.clear();
  });

  it('read the contract routes', async () => {
    get.mockResolvedValue({ status: 501, data: null });
    await loadFleetSummary();
    await loadFleetNodes();
    const paths = get.mock.calls.map((c) => c[0] as string);
    expect(paths).toEqual(['/v1/fleet/summary', '/v1/fleet/nodes']);
    const known = new Set<string>(Object.values(UBAG_ENDPOINTS).map((e) => e.path));
    for (const p of paths) expect(known.has(p)).toBe(true);
  });

  it('return data only on 200', async () => {
    get.mockResolvedValueOnce({ status: 200, data: summary });
    expect((await loadFleetSummary())?.nodes_total).toBe(3);
    get.mockResolvedValueOnce({ status: 200, data: { kind: 'fleet_nodes', total: 1, data: [node()] } });
    expect((await loadFleetNodes())?.map((n) => n.node_id)).toEqual(['helper-1']);
  });

  it('hide the panel on 501, 404, 403, a failure or a body that is not the contract', async () => {
    for (const status of [501, 404, 403, 500, -1]) {
      snapshots.clear();
      get.mockResolvedValueOnce({ status, data: summary });
      expect(await loadFleetSummary(), `status ${status}`).toBeNull();
      get.mockResolvedValueOnce({ status, data: { data: [node()] } });
      expect(await loadFleetNodes(), `status ${status}`).toBeNull();
    }
    snapshots.clear();
    get.mockResolvedValueOnce({ status: 200, data: { hello: 'world' } });
    expect(await loadFleetSummary()).toBeNull();
  });

  it('share a read within the snapshot window and re-read on force', async () => {
    get.mockResolvedValue({ status: 200, data: summary });
    await loadFleetSummary();
    await loadFleetSummary();
    expect(get).toHaveBeenCalledTimes(1);
    await loadFleetSummary(true);
    expect(get).toHaveBeenCalledTimes(2);
  });
});
