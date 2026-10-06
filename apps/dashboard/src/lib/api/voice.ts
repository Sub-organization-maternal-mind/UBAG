import { snapshots } from '../stores/snapshot';
import { api } from './client';
import { EM_DASH, QUEUE_REASON_HINT, humanize } from './fleet';
import type { VoiceCapability, VoiceSessionRow, VoiceTargetRow } from './types';

/**
 * Voice sessions and per-target voice capabilities (GET /v1/voice/sessions,
 * GET /v1/capabilities). The panel renders only when BOTH answer 200 with the
 * contract shape: 501 (voice not configured), 404 (older gateway), 403 and any
 * failure all read as "no panel", never as an error. Nothing the gateway did not
 * report is invented; an unreported value is an em dash.
 */

type UnknownRecord = Record<string, unknown>;

function record(value: unknown): UnknownRecord {
  return value != null && typeof value === 'object' && !Array.isArray(value)
    ? (value as UnknownRecord)
    : {};
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

/** The capability records that advertise live voice; null when the body is not the contract shape. */
export function parseVoiceTargets(body: unknown): VoiceTargetRow[] | null {
  const raw = record(body);
  if (!Array.isArray(raw.data)) return null;
  const rows: VoiceTargetRow[] = [];
  for (const entry of raw.data) {
    const e = record(entry);
    const v = record(e.voice);
    const supported = v.supported ?? v.live; // `live` is the legacy alias of `supported`
    if (typeof e.target !== 'string' || !e.target || supported !== true) continue;
    const voice: VoiceCapability = {
      supported: true,
      configured: v.configured === true,
      verified: v.verified === true,
      verified_note: str(v.verified_note),
      available: v.available === true,
      free_resources: isNum(v.free_resources)
        ? v.free_resources
        : isNum(v.available_accounts)
          ? v.available_accounts
          : 0,
    };
    rows.push({ target: e.target, display_name: str(e.display_name) || e.target, voice });
  }
  return rows.sort((a, b) => a.target.localeCompare(b.target));
}

/** The sessions of a voice-session list body, in the order served; null when it is not one. */
export function parseVoiceSessions(body: unknown): VoiceSessionRow[] | null {
  const raw = record(body);
  if (!Array.isArray(raw.data)) return null;
  return raw.data
    .map(record)
    .filter((s) => typeof s.session_id === 'string' && s.session_id !== '' && typeof s.status === 'string')
    .map((s) => ({
      session_id: s.session_id as string,
      target: str(s.target),
      mode: str(s.mode),
      status: s.status as string,
      muted: s.muted === true,
      identity_ref: str(s.identity_ref),
      instance_ref: str(s.instance_ref),
      created_at: str(s.created_at),
      updated_at: str(s.updated_at),
      queue_reason: str(s.queue_reason),
      node_label: str(s.node_label),
    }));
}

/**
 * Why a queued session is waiting: the gateway's reason when it sends one, else
 * the one fact a queued session always means (no authenticated account with a
 * free browser environment at that moment). Null for any other status.
 */
export function voiceQueueReason(s: VoiceSessionRow): { label: string; hint: string } | null {
  if (s.status !== 'queued') return null;
  if (s.queue_reason) {
    return { label: humanize(s.queue_reason), hint: QUEUE_REASON_HINT[s.queue_reason] ?? '' };
  }
  return {
    label: 'Waiting for account',
    hint: 'No authenticated provider account with a free browser environment was available. Connect promotes the session when one frees up.',
  };
}

/** Where the call is hosted: the gateway's node label, else "identity · instance"; never an address. */
export function voicePlacement(s: VoiceSessionRow): string {
  if (s.node_label) return s.node_label;
  const parts = [s.identity_ref, s.instance_ref].filter(Boolean);
  return parts.length ? parts.join(' · ') : EM_DASH;
}

export const yesNo = (v: boolean): string => (v ? 'Yes' : 'No');

export interface VoicePanel {
  targets: VoiceTargetRow[];
  sessions: VoiceSessionRow[];
}

async function read<T>(path: string, parse: (body: unknown) => T | null, force: boolean): Promise<T | null> {
  const res = await snapshots.get(path, () => api.get<unknown>(path), { ttlMs: 10_000, force });
  return res.status === 200 ? parse(res.data) : null;
}

/** Both reads, or null when either is unavailable (hide the panel). */
export async function loadVoicePanel(force = false): Promise<VoicePanel | null> {
  const [targets, sessions] = await Promise.all([
    read('/v1/capabilities', parseVoiceTargets, force),
    read('/v1/voice/sessions?limit=25', parseVoiceSessions, force),
  ]);
  return targets && sessions ? { targets, sessions } : null;
}
