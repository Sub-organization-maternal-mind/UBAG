/**
 * UBAG Gateway client — fetches live data from the gateway API and maps it to
 * the dashboard data shape. Falls back gracefully to null on failure so mock
 * data can still be shown.
 */

const GATEWAY_BASE =
  localStorage.getItem('ubag_gateway_url') || window.location.origin || 'http://127.0.0.1:8081';
const APP_SECRET =
  localStorage.getItem('ubag_app_secret') || '';
const API_VERSION = '2026-05-22';

export function getConfig() {
  return {
    gatewayUrl: GATEWAY_BASE,
    appSecret: APP_SECRET ? '••••' + APP_SECRET.slice(-6) : '(not set)',
    isConfigured: APP_SECRET.length > 0
  };
}

export function saveConfig(url, secret) {
  localStorage.setItem('ubag_gateway_url', url.replace(/\/+$/, ''));
  localStorage.setItem('ubag_app_secret', secret);
  window.location.reload();
}

async function gw(method, path, body) {
  const headers = {
    'Ubag-Api-Version': API_VERSION,
    'Content-Type': 'application/json'
  };
  const secret = localStorage.getItem('ubag_app_secret') || '';
  if (secret) headers['Authorization'] = `Bearer ${secret}`;
  if (method !== 'GET' && method !== 'HEAD') {
    headers['Idempotency-Key'] = crypto.randomUUID();
  }
  const url = (localStorage.getItem('ubag_gateway_url') || GATEWAY_BASE) + path;
  const response = await fetch(url, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined
  });
  const text = await response.text();
  return { status: response.status, data: text ? JSON.parse(text) : null };
}

export async function fetchHealth() {
  try {
    const { data } = await gw('GET', '/v1/health');
    return data;
  } catch { return null; }
}

export async function fetchReady() {
  try {
    const { data } = await gw('GET', '/v1/health');
    // /v1/ready is blocked at caddy edge, use /v1/health
    return data;
  } catch { return null; }
}

export async function fetchVersion() {
  try {
    const { data } = await gw('GET', '/v1/health');
    return data;
  } catch { return null; }
}

export async function fetchJobs() {
  try {
    const { status, data } = await gw('GET', '/v1/jobs');
    if (status === 200 && data && data.jobs) return data.jobs;
    return null;
  } catch { return null; }
}

export async function fetchTargets() {
  try {
    const { status, data } = await gw('GET', '/v1/targets');
    if (status === 200 && data && data.items) return data.items;
    return null;
  } catch { return null; }
}

export async function fetchAdapters() {
  try {
    const { status, data } = await gw('GET', '/v1/adapters');
    if (status === 200 && data && data.items) return data.items;
    return null;
  } catch { return null; }
}

export async function fetchTemplates() {
  try {
    const { status, data } = await gw('GET', '/v1/templates');
    if (status === 200 && data && data.items) return data.items;
    return null;
  } catch { return null; }
}

export async function fetchWorkflows() {
  try {
    const { status, data } = await gw('GET', '/v1/workflows');
    if (status === 200 && data && data.items) return data.items;
    return null;
  } catch { return null; }
}

export async function fetchCache() {
  try {
    const { status, data } = await gw('GET', '/v1/cache');
    if (status === 200) return data;
    return null;
  } catch { return null; }
}

export async function fetchRateLimits() {
  try {
    const { status, data } = await gw('GET', '/v1/rate-limits');
    if (status === 200) return data;
    return { denied: true };
  } catch { return null; }
}

export async function fetchAudit() {
  try {
    const { status, data } = await gw('GET', '/v1/audit');
    if (status === 200 && data && data.items) return data.items;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function fetchSSOConfig() {
  try {
    const { status, data } = await gw('GET', '/v1/sso/config');
    if (status === 200) return data;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function fetchBrowserSummary() {
  try {
    const { status, data } = await gw('GET', '/v1/browser/summary');
    if (status === 200) return data;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function fetchBrowserInstances() {
  try {
    const { status, data } = await gw('GET', '/v1/browser/instances');
    if (status === 200 && data) return data.data || data.items || null;
    return null;
  } catch { return null; }
}

export async function fetchBrowserContexts() {
  try {
    const { status, data } = await gw('GET', '/v1/browser/contexts');
    if (status === 200 && data) return data.data || data.items || null;
    return null;
  } catch { return null; }
}

export async function fetchBrowserTabs() {
  try {
    const { status, data } = await gw('GET', '/v1/browser/tabs');
    if (status === 200 && data) return data.data || data.items || null;
    return null;
  } catch { return null; }
}

export async function fetchConcurrency() {
  try {
    const { status, data } = await gw('GET', '/v1/concurrency');
    if (status === 200 && data) return data.data || data.items || null;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function fetchAlerts() {
  try {
    const { status, data } = await gw('GET', '/v1/alerts');
    if (status === 200 && data) return data.data || data.items || null;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function fetchAlertConfig() {
  try {
    const { status, data } = await gw('GET', '/v1/alerts/config');
    if (status === 200) return data;
    return status === 403 ? { denied: true } : null;
  } catch { return null; }
}

export async function acknowledgeAlert(alertId) {
  try {
    const { status, data } = await gw('POST', `/v1/alerts/${alertId}/acknowledge`, {
      actor: 'dashboard operator'
    });
    return { status, data };
  } catch (err) { return { status: -1, data: { error: { message: err.message } } }; }
}

export async function resolveAlert(alertId) {
  try {
    const { status, data } = await gw('POST', `/v1/alerts/${alertId}/resolve`, {
      actor: 'dashboard operator',
      resolution: 'human-solved'
    });
    return { status, data };
  } catch (err) { return { status: -1, data: { error: { message: err.message } } }; }
}

export async function createJob(target, commandType, input) {
  try {
    const body = {
      job: { target, command_type: commandType, input },
      client: {
        app_id: 'ubag-dashboard',
        app_version: '1.0.0',
        sdk: { name: 'dashboard', version: '1.0.0' }
      }
    };
    const { status, data } = await gw('POST', '/v1/jobs', body);
    return { status, data };
  } catch (err) { return { status: -1, data: { error: { message: err.message } } }; }
}

export async function cancelJob(jobId) {
  try {
    const { status, data } = await gw('POST', `/v1/jobs/${jobId}:cancel`, { reason: 'dashboard operator' });
    return { status, data };
  } catch (err) { return { status: -1, data: { error: { message: err.message } } }; }
}

/**
 * Fetch all live data in parallel and return a structured snapshot.
 */
export async function fetchDashboardSnapshot() {
  const [health, jobs, targets, adapters, templates, workflows, cache, rateLimits, audit, sso, browserSummary, browserInstances, concurrency, alerts, alertConfig] =
    await Promise.allSettled([
      fetchHealth(),
      fetchJobs(),
      fetchTargets(),
      fetchAdapters(),
      fetchTemplates(),
      fetchWorkflows(),
      fetchCache(),
      fetchRateLimits(),
      fetchAudit(),
      fetchSSOConfig(),
      fetchBrowserSummary(),
      fetchBrowserInstances(),
      fetchConcurrency(),
      fetchAlerts(),
      fetchAlertConfig()
    ]);

  return {
    health: health.status === 'fulfilled' ? health.value : null,
    jobs: jobs.status === 'fulfilled' ? jobs.value : null,
    targets: targets.status === 'fulfilled' ? targets.value : null,
    adapters: adapters.status === 'fulfilled' ? adapters.value : null,
    templates: templates.status === 'fulfilled' ? templates.value : null,
    workflows: workflows.status === 'fulfilled' ? workflows.value : null,
    cache: cache.status === 'fulfilled' ? cache.value : null,
    rateLimits: rateLimits.status === 'fulfilled' ? rateLimits.value : null,
    audit: audit.status === 'fulfilled' ? audit.value : null,
    sso: sso.status === 'fulfilled' ? sso.value : null,
    browserSummary: browserSummary.status === 'fulfilled' ? browserSummary.value : null,
    browserInstances: browserInstances.status === 'fulfilled' ? browserInstances.value : null,
    concurrency: concurrency.status === 'fulfilled' ? concurrency.value : null,
    alerts: alerts.status === 'fulfilled' ? alerts.value : null,
    alertConfig: alertConfig.status === 'fulfilled' ? alertConfig.value : null,
    fetchedAt: new Date().toLocaleString()
  };
}
