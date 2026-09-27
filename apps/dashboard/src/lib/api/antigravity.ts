import { gw, listOf } from './client';

export interface AntigravityAccount {
  account_id: string;
  label: string;
  enabled: boolean;
  tier: string;
  last_used: string;
  cooldown_until: string | null;
  created_at: string;
  worker_socket_present: boolean;
  verification_state?: 'unverified' | 'pending' | 'verified' | 'failed';
  verification_job_id?: string;
  verified_at?: string;
}

export interface AntigravityLoginSession {
  state: 'not_started' | 'starting' | 'awaiting_code' | 'verifying' | 'closed' | 'stopped';
  authorization_url?: string;
}

async function accountLogin(id: string, method: 'GET' | 'POST' | 'DELETE', body?: unknown): Promise<AntigravityLoginSession> {
  const res = await gw<AntigravityLoginSession>(
    method, `/v1/antigravity/accounts/${encodeURIComponent(id)}/login`, body
  );
  if (res.data && !res.error) return res.data;
  throw new Error(res.error || 'Login session unavailable');
}

export const getAntigravityLogin = (id: string) => accountLogin(id, 'GET');
export const startAntigravityLogin = (id: string) => accountLogin(id, 'POST', { action: 'start' });
export const submitAntigravityCode = (id: string, input: string) => accountLogin(id, 'POST', { action: 'input', input });
export const stopAntigravityLogin = (id: string) => accountLogin(id, 'DELETE');

export interface AntigravityConfig {
  default_model: string;
  default_effort: string;
  max_concurrent: number;
  account_count: number;
  oauth_enabled?: boolean;
}

export async function listAntigravityAccounts(): Promise<AntigravityAccount[]> {
  const res = await gw<AntigravityAccount[]>('GET', '/v1/antigravity/accounts');
  if (res.error) throw new Error(res.error);
  return listOf<AntigravityAccount>(res, 'accounts');
}

export async function addAntigravityAccount(input: {
  label: string;
  tier: string;
}): Promise<AntigravityAccount> {
  const res = await gw<AntigravityAccount>('POST', '/v1/antigravity/accounts', input);
  if (res.data) return res.data;
  throw new Error(res.error || 'Failed to add account');
}

export async function removeAntigravityAccount(id: string): Promise<void> {
  const res = await gw('DELETE', `/v1/antigravity/accounts/${encodeURIComponent(id)}`);
  if (res.error) throw new Error(res.error);
}

export async function updateAntigravityAccount(
  id: string,
  patch: { enabled?: boolean; label?: string; tier?: string }
): Promise<AntigravityAccount> {
  const res = await gw<AntigravityAccount>(
    'PUT',
    `/v1/antigravity/accounts/${encodeURIComponent(id)}`,
    patch
  );
  if (res.data) return res.data;
  throw new Error(res.error || 'Failed to update account');
}

export async function getAntigravityConfig(): Promise<AntigravityConfig> {
  const res = await gw<AntigravityConfig>('GET', '/v1/antigravity/config');
  if (res.data) return res.data;
  throw new Error(res.error || 'Failed to get config');
}

export async function updateAntigravityConfig(
  config: Partial<Pick<AntigravityConfig, 'default_model' | 'default_effort' | 'max_concurrent'>>
): Promise<AntigravityConfig> {
  const res = await gw<AntigravityConfig>('PUT', '/v1/antigravity/config', config);
  if (res.data) return res.data;
  throw new Error(res.error || 'Failed to update config');
}

export async function testAntigravity(
  prompt: string,
  accountID: string,
  model?: string
): Promise<{ job_id: string; status: string; model: string }> {
  const res = await gw<{ job_id: string; status: string; model: string }>(
    'POST',
    '/v1/antigravity/test',
    { prompt, account_id: accountID, model }
  );
  if (res.data) return res.data;
  throw new Error(res.error || 'Failed to test');
}
