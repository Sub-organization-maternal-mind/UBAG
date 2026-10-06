import { beforeEach, describe, expect, it, vi } from 'vitest';
import { UBAG_ENDPOINTS } from '../../../../../packages/sdk-typescript/src/generated/contract-manifest';

const get = vi.fn();
vi.mock('./client', () => ({ api: { get: (...a: unknown[]) => get(...a) } }));

import { snapshots } from '../stores/snapshot';
import {
  loadVoicePanel,
  parseVoiceSessions,
  parseVoiceTargets,
  voicePlacement,
  voiceQueueReason,
  yesNo,
} from './voice';

const cap = (target: string, voice: Record<string, unknown>) => ({
  target,
  display_name: target.toUpperCase(),
  voice,
});

const targets = {
  kind: 'capabilities',
  data: [
    cap('gemini_web', {
      live: true, supported: true, configured: true, verified: false, verified_note: 'not yet run',
      available: true, free_resources: 2, available_accounts: 2,
    }),
    cap('mock', { live: false, supported: false, configured: false, verified: false, available: false, free_resources: 0 }),
    cap('chatgpt_web', {
      live: true, supported: true, configured: false, verified: true, verified_note: 'run 2026-09-30',
      available: false, free_resources: 0,
    }),
  ],
};

const session = (over: Record<string, unknown> = {}) => ({
  session_id: 'vs_0123456789abcdef',
  tenant_id: 't', app_id: 'a',
  target: 'gemini_web', mode: 'live', status: 'connected', muted: false,
  identity_ref: 'acct-1', instance_ref: 'inst-1',
  created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:01:00Z',
  ...over,
});

describe('parseVoiceTargets', () => {
  it('keeps only voice-supported targets, sorted, with the four flags and the free count', () => {
    const rows = parseVoiceTargets(targets)!;
    expect(rows.map((r) => r.target)).toEqual(['chatgpt_web', 'gemini_web']);
    expect(rows[0].voice).toMatchObject({ supported: true, configured: false, verified: true, verified_note: 'run 2026-09-30', available: false, free_resources: 0 });
    expect(rows[1].voice).toMatchObject({ supported: true, configured: true, verified: false, available: true, free_resources: 2 });
  });

  it('reads the legacy aliases when the new fields are absent', () => {
    const [row] = parseVoiceTargets({ data: [cap('x', { live: true, available_accounts: 3 })] })!;
    expect(row.voice).toMatchObject({ supported: true, configured: false, verified: false, available: false, free_resources: 3 });
  });

  it('never reads a missing flag as true', () => {
    const [row] = parseVoiceTargets({ data: [cap('x', { supported: true })] })!;
    expect(row.voice).toMatchObject({ configured: false, verified: false, available: false, free_resources: 0 });
  });

  it('is null when the body has no data list, and skips malformed entries', () => {
    expect(parseVoiceTargets(null)).toBeNull();
    expect(parseVoiceTargets({ data: 'x' })).toBeNull();
    expect(parseVoiceTargets({ data: [null, 'x', { target: '', voice: { supported: true } }, { voice: { supported: true } }] })).toEqual([]);
  });
});

describe('parseVoiceSessions', () => {
  it('maps the wire session and skips rows without an id or status', () => {
    const rows = parseVoiceSessions({ data: [session(), session({ session_id: '' }), session({ status: undefined }), null] })!;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ session_id: 'vs_0123456789abcdef', status: 'connected', muted: false, identity_ref: 'acct-1', queue_reason: '', node_label: '' });
  });

  it('is null when the body is not a list', () => {
    expect(parseVoiceSessions({ error: 'nope' })).toBeNull();
    expect(parseVoiceSessions(undefined)).toBeNull();
  });
});

describe('queued reason', () => {
  const row = (over: Record<string, unknown>) => parseVoiceSessions({ data: [session(over)] })![0];

  it('only a queued session has one', () => {
    for (const status of ['connecting', 'connected', 'terminated']) expect(voiceQueueReason(row({ status }))).toBeNull();
  });

  it('falls back to the one fact a queued session means', () => {
    const r = voiceQueueReason(row({ status: 'queued' }))!;
    expect(r.label).toBe('Waiting for account');
    expect(r.hint).toMatch(/free browser environment/);
  });

  it('prefers the gateway reason, with its hint for a known coarse reason and none for an unknown one', () => {
    expect(voiceQueueReason(row({ status: 'queued', queue_reason: 'waiting_for_node' }))).toEqual({
      label: 'Waiting for node',
      hint: 'Tied to a helper node that is not available right now.',
    });
    expect(voiceQueueReason(row({ status: 'queued', queue_reason: 'something_new' }))).toEqual({ label: 'Something new', hint: '' });
  });
});

describe('placement label', () => {
  const row = (over: Record<string, unknown>) => parseVoiceSessions({ data: [session(over)] })![0];

  it('shows the identity and instance the session holds', () => {
    expect(voicePlacement(row({}))).toBe('acct-1 · inst-1');
    expect(voicePlacement(row({ instance_ref: '' }))).toBe('acct-1');
  });

  it('prefers a node label the gateway sends', () => {
    expect(voicePlacement(row({ node_label: 'helper-1' }))).toBe('helper-1');
  });

  it('is an em dash for a queued session that holds nothing', () => {
    expect(voicePlacement(row({ status: 'queued', identity_ref: '', instance_ref: '' }))).toBe('—');
  });
});

it('yesNo', () => {
  expect([yesNo(true), yesNo(false)]).toEqual(['Yes', 'No']);
});

describe('loadVoicePanel', () => {
  beforeEach(() => {
    get.mockReset();
    snapshots.clear();
  });

  const reply = (caps: { status: number; data?: unknown }, sess: { status: number; data?: unknown }) =>
    get.mockImplementation(async (path: string) => (path.startsWith('/v1/capabilities') ? caps : sess));

  it('reads the contract routes', async () => {
    reply({ status: 501 }, { status: 501 });
    await loadVoicePanel();
    const paths = get.mock.calls.map((c) => (c[0] as string).split('?')[0]).sort();
    expect(paths).toEqual(['/v1/capabilities', '/v1/voice/sessions']);
    const known = new Set<string>(Object.values(UBAG_ENDPOINTS).map((e) => e.path));
    for (const p of paths) expect(known.has(p)).toBe(true);
  });

  it('returns both lists on 200', async () => {
    reply({ status: 200, data: targets }, { status: 200, data: { data: [session()] } });
    const panel = await loadVoicePanel();
    expect(panel?.targets).toHaveLength(2);
    expect(panel?.sessions).toHaveLength(1);
  });

  it('is null (panel hidden) when either read is 501, 404, 403, a failure or not the contract', async () => {
    for (const status of [501, 404, 403, 500, -1]) {
      snapshots.clear();
      reply({ status: 200, data: targets }, { status, data: { data: [] } });
      expect(await loadVoicePanel(), `voice ${status}`).toBeNull();
      snapshots.clear();
      reply({ status, data: targets }, { status: 200, data: { data: [] } });
      expect(await loadVoicePanel(), `capabilities ${status}`).toBeNull();
    }
    snapshots.clear();
    reply({ status: 200, data: {} }, { status: 200, data: {} });
    expect(await loadVoicePanel()).toBeNull();
  });

  it('shares a read within the snapshot window and re-reads on force', async () => {
    reply({ status: 200, data: targets }, { status: 200, data: { data: [] } });
    await loadVoicePanel();
    await loadVoicePanel();
    expect(get).toHaveBeenCalledTimes(2);
    await loadVoicePanel(true);
    expect(get).toHaveBeenCalledTimes(4);
  });
});
