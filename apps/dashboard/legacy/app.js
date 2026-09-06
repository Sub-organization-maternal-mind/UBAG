import { dashboardData, dashboardTabs } from './mock-data.js';
import { getConfig, saveConfig, fetchDashboardSnapshot, createJob, cancelJob, acknowledgeAlert, resolveAlert } from './gateway-client.js';

const tabButtons = Array.from(document.querySelectorAll('[role="tab"]'));
const panelStack = document.querySelector('#dashboard-panel');
const lastSync = document.querySelector('#last-sync');
const refreshStatus = document.querySelector('#refresh-status');
const statusTitle = document.querySelector('#status-title');
const sourcePill = document.querySelector('.source-pill');

// --- Gateway connection state ---
const config = getConfig();
let liveSnapshot = null;

if (config.isConfigured) {
  statusTitle.textContent = 'Connecting to gateway…';
  sourcePill.textContent = 'Live: ' + config.gatewayUrl;
  fetchDashboardSnapshot().then((snap) => {
    liveSnapshot = snap;
    if (snap.health) {
      statusTitle.textContent = `Gateway: ${snap.health.status} (${snap.health.version || 'unknown'})`;
      lastSync.textContent = `Live sync ${snap.fetchedAt}`;
      refreshStatus.textContent = 'Dashboard is connected to the live gateway.';
    } else {
      statusTitle.textContent = 'Gateway unreachable — showing mock data';
    }
    renderPanels();
  });
} else {
  sourcePill.textContent = 'Archived local data';
}

lastSync.textContent = `Last sync ${dashboardData.lastSync}`;

const titleCase = (value) => value.charAt(0).toUpperCase() + value.slice(1);

const statusTone = (status) =>
  ({
    Ready: 'ready',
    'Needs review': 'review',
    Blocked: 'blocked',
    Draft: 'draft',
    Contracted: 'review',
    Implemented: 'ready',
    'External activation': 'review',
    'Not yet served': 'draft',
    'Not connected': 'muted',
    ready: 'ready',
    busy: 'review',
    warming: 'review',
    draining: 'draft',
    quarantined: 'blocked',
    open: 'review',
    acknowledged: 'draft',
    resolved: 'ready'
  })[status] || 'muted';

const badge = (status) =>
  `<span class="status-badge" data-tone="${statusTone(status)}">${status}</span>`;

const emptyState = (heading, detail) => `
  <div class="empty-state">
    <div class="pattern-tile" aria-hidden="true"></div>
    <div>
      <h3>${heading}</h3>
      <p>${detail}</p>
    </div>
  </div>
`;

const stateNotice = (item) => `
  <article
    class="state-notice state-${item.state}"
    role="${item.state === 'error' || item.state === 'permission-denied' ? 'alert' : 'status'}"
    aria-live="${item.state === 'error' || item.state === 'permission-denied' ? 'assertive' : 'polite'}"
    aria-label="${item.aria}"
    ${item.state === 'loading' ? 'aria-busy="true"' : ''}
  >
    <span class="state-dot" aria-hidden="true"></span>
    <div>
      <h3>${item.title}</h3>
      <p>${item.detail}</p>
    </div>
  </article>
`;

const table = (columns, rows) => `
  <div class="table-wrap">
    <table class="data-table">
      <thead>
        <tr>${columns.map((column) => `<th scope="col">${column.label}</th>`).join('')}</tr>
      </thead>
      <tbody>
        ${rows
          .map(
            (row) => `
              <tr>
                ${columns
                  .map((column) => {
                    const value = row[column.key];
                    const rendered =
                      column.key === 'status' ||
                      column.key === 'health' ||
                      column.key === 'drift' ||
                      column.key === 'readiness' ||
                      column.key === 'state'
                        ? badge(value)
                        : value;

                    return `<td data-label="${column.label}">${rendered}</td>`;
                  })
                  .join('')}
              </tr>
            `
          )
          .join('')}
      </tbody>
    </table>
  </div>
`;

const panelFrame = (id, heading, detail, body) => `
  <section
    id="panel-${id}"
    class="view-panel"
    role="tabpanel"
    tabindex="0"
    aria-labelledby="tab-${id}"
    ${id === 'overview' ? '' : 'hidden'}
  >
    <div class="panel-heading">
      <div>
        <p class="section-kicker">${titleCase(id)}</p>
        <h2>${heading}</h2>
      </div>
      <p>${detail}</p>
    </div>
    ${body}
  </section>
`;

const renderOverview = () => {
  // Build live summary cards when connected
  let metrics;
  if (liveSnapshot && liveSnapshot.health) {
    const h = liveSnapshot.health;
    const jobCount = liveSnapshot.jobs ? liveSnapshot.jobs.length : '?';
    const targetCount = liveSnapshot.targets ? liveSnapshot.targets.length : '?';
    const templateCount = liveSnapshot.templates ? liveSnapshot.templates.length : '?';
    metrics = [
      { label: 'Gateway', value: h.status || 'ok', state: h.status === 'ok' ? 'Ready' : 'Needs review', source: h.service || 'gateway', tone: h.status === 'ok' ? 'ready' : 'blocked' },
      { label: 'Jobs', value: String(jobCount), state: jobCount > 0 ? 'Ready' : 'Draft', source: 'Live /v1/jobs', tone: 'ready' },
      { label: 'Targets', value: String(targetCount), state: 'Ready', source: 'Live /v1/targets', tone: 'ready' },
      { label: 'Templates', value: String(templateCount), state: templateCount > 0 ? 'Ready' : 'Draft', source: 'Live /v1/templates', tone: templateCount > 0 ? 'ready' : 'review' }
    ].map(
      (item) => `
        <article class="metric-card" data-tone="${item.tone}">
          <div>
            <p>${item.label}</p>
            <strong>${item.value}</strong>
          </div>
          ${badge(item.state)}
          <span>Source: ${item.source}</span>
        </article>
      `
    ).join('');
  } else {
    metrics = dashboardData.summary
      .map(
        (item) => `
          <article class="metric-card" data-tone="${item.tone}">
            <div>
              <p>${item.label}</p>
              <strong>${item.value}</strong>
            </div>
            ${badge(item.state)}
            <span>Source: ${item.source}</span>
          </article>
        `
      )
      .join('');
  }

  const items = dashboardData.operatorItems
    .map(
      (item) => `
        <li class="work-item">
          <div>
            <h3>${item.title}</h3>
            <p>${item.owner} · ${item.nextStep}</p>
          </div>
          ${badge(item.status)}
        </li>
      `
    )
    .join('');

  const activity = dashboardData.activity
    .map(
      (event) => `
        <li class="activity-item">
          <time>${event.time}</time>
          <span>${event.label}</span>
          ${badge(event.status)}
        </li>
      `
    )
    .join('');

  const stateFixtures = dashboardData.stateFixtures.map(stateNotice).join('');

  return panelFrame(
    'overview',
    'Workspace state',
    'Compact status for the current local edge slice.',
    `
      <div class="metric-grid">${metrics}</div>
      <div class="split-layout">
        <section class="list-panel" aria-labelledby="operator-items-title">
          <div class="list-heading">
            <h3 id="operator-items-title">Operator items</h3>
            <span>Manual follow-up</span>
          </div>
          <ul class="work-list">${items}</ul>
        </section>
        <section class="list-panel" aria-labelledby="activity-title">
          <div class="list-heading">
            <h3 id="activity-title">Latest activity</h3>
            <span>Mock stream</span>
          </div>
          <ol class="activity-list">${activity}</ol>
        </section>
      </div>
      <section class="state-fixture-panel" aria-labelledby="state-fixtures-title">
        <div class="list-heading">
          <h3 id="state-fixtures-title">Reachable state coverage</h3>
          <span>Accessibility fixtures</span>
        </div>
        <div class="state-fixture-grid">${stateFixtures}</div>
      </section>
    `
  );
};

const renderApps = () =>
  panelFrame(
    'apps',
    'App access',
    'Local app registrations and their next safe action.',
    table(
      [
        { key: 'name', label: 'App' },
        { key: 'environment', label: 'Environment' },
        { key: 'auth', label: 'Auth' },
        { key: 'quota', label: 'Quota' },
        { key: 'status', label: 'Status' },
        { key: 'nextStep', label: 'Next step' }
      ],
      dashboardData.apps
    )
  );

const renderTargets = () => {
  const liveTargets = liveSnapshot && liveSnapshot.targets ? liveSnapshot.targets.map(t => ({
    name: t.name || t.target_id || '—',
    adapter: t.adapter || t.adapter_id || '—',
    drift: t.drift || 'unknown',
    login: t.login || t.auth_status || '—',
    health: t.health || t.status || '—',
    nextStep: t.next_step || '—'
  })) : null;

  return panelFrame(
    'targets',
    liveTargets ? `Automation targets (Live)` : 'Automation targets',
    liveTargets ? 'Live target data from /v1/targets.' : 'Adapter readiness, login posture, and drift state.',
    table(
      [
        { key: 'name', label: 'Target' },
        { key: 'adapter', label: 'Adapter' },
        { key: 'drift', label: 'Drift' },
        { key: 'login', label: 'Login' },
        { key: 'health', label: 'Health' },
        { key: 'nextStep', label: 'Next step' }
      ],
      liveTargets || dashboardData.targets
    )
  );
};

const renderJobs = () => {
  const liveJobs = liveSnapshot && liveSnapshot.jobs ? liveSnapshot.jobs.map(j => ({
    id: j.job_id || j.id || '—',
    app: j.app_id || j.app || '—',
    target: j.target || '—',
    status: j.status || 'unknown',
    retry: j.retry_count != null ? String(j.retry_count) : '—',
    idempotency: j.idempotency_key ? j.idempotency_key.slice(0, 8) + '…' : '—',
    updated: j.updated_at || j.created_at || '—'
  })) : null;

  return panelFrame(
    'jobs',
    liveJobs ? `Job queue (Live — ${liveJobs.length})` : 'Job queue',
    liveJobs ? 'Live job queue from /v1/jobs.' : 'Queue snapshot with idempotency, retry, and target context.',
    table(
      [
        { key: 'id', label: 'Job' },
        { key: 'app', label: 'App' },
        { key: 'target', label: 'Target' },
        { key: 'status', label: 'Status' },
        { key: 'retry', label: 'Retry' },
        { key: 'idempotency', label: 'Idempotency' },
        { key: 'updated', label: 'Updated' }
      ],
      liveJobs || dashboardData.jobs
    )
  );
};

const renderSessions = () =>
  panelFrame(
    'sessions',
    'Browser sessions',
    'Worker-owned browser state, operator posture, and artifacts.',
    table(
      [
        { key: 'id', label: 'Session' },
        { key: 'target', label: 'Target' },
        { key: 'browser', label: 'Browser' },
        { key: 'status', label: 'Status' },
        { key: 'operator', label: 'Operator' },
        { key: 'artifact', label: 'Artifact' }
      ],
      dashboardData.sessions
    )
  );

const renderTemplates = () => {
  const liveTemplates = liveSnapshot && liveSnapshot.templates ? liveSnapshot.templates.map(t => ({
    name: t.name || t.template_id || '—',
    mode: t.mode || t.type || '—',
    variables: t.variables ? String(t.variables.length || Object.keys(t.variables).length) : '0',
    status: t.status || 'draft',
    nextStep: t.next_step || '—'
  })) : null;

  const rows = liveTemplates || dashboardData.templates;
  return panelFrame(
    'templates',
    liveTemplates ? `Template library (Live — ${liveTemplates.length})` : 'Template library',
    liveTemplates ? 'Live template data from /v1/templates.' : 'Early workflow templates with explicit draft and review states.',
    rows.length
      ? table(
          [
            { key: 'name', label: 'Template' },
            { key: 'mode', label: 'Mode' },
            { key: 'variables', label: 'Variables' },
            { key: 'status', label: 'Status' },
            { key: 'nextStep', label: 'Next step' }
          ],
          rows
        )
      : emptyState(
          'No templates yet',
          'Add a local template seed before enabling live workflow execution.'
        )
  );
};

const renderRuntime = () => {
  // Show live health/runtime info when available
  const runtimeRows = liveSnapshot && liveSnapshot.health ? [
    { surface: 'Gateway', mode: 'Live', readiness: liveSnapshot.health.status || 'ok', source: '/v1/health', nextStep: '—' },
    { surface: 'Cache', mode: liveSnapshot.cache ? 'Active' : 'Unknown', readiness: liveSnapshot.cache ? 'ready' : '?', source: '/v1/cache', nextStep: '—' },
    { surface: 'Rate-limits', mode: liveSnapshot.rateLimits && !liveSnapshot.rateLimits.denied ? 'Active' : 'Denied/Unknown', readiness: liveSnapshot.rateLimits && !liveSnapshot.rateLimits.denied ? 'ready' : '?', source: '/v1/rate-limits', nextStep: '—' },
    { surface: 'SSO', mode: liveSnapshot.sso && !liveSnapshot.sso.denied ? 'Configured' : 'N/A', readiness: liveSnapshot.sso && !liveSnapshot.sso.denied ? 'ready' : '—', source: '/v1/sso/config', nextStep: '—' },
    { surface: 'Audit', mode: liveSnapshot.audit && !liveSnapshot.audit.denied ? 'Active' : 'Denied/N/A', readiness: liveSnapshot.audit && !liveSnapshot.audit.denied ? 'ready' : '—', source: '/v1/audit', nextStep: '—' }
  ] : dashboardData.runtime;

  return panelFrame(
    'runtime',
    liveSnapshot && liveSnapshot.health ? 'Runtime readiness (Live)' : 'Runtime readiness',
    'Read-only operator map for gateway, storage, executor, artifacts, webhooks, and event-stream status.',
    table(
      [
        { key: 'surface', label: 'Surface' },
        { key: 'mode', label: 'Mode' },
        { key: 'readiness', label: 'Readiness' },
        { key: 'source', label: 'Evidence' },
        { key: 'nextStep', label: 'Next step' }
      ],
      runtimeRows
    )
  );
};

const renderActivation = () =>
  panelFrame(
    'activation',
    'Activation and roadmap',
    'Explicit distinction between implemented code, contracted runtime, external activation, and not-yet-served protocols.',
    table(
      [
        { key: 'area', label: 'Area' },
        { key: 'state', label: 'State' },
        { key: 'operatorInput', label: 'Operator input' },
        { key: 'guardrail', label: 'Guardrail' }
      ],
      dashboardData.activation
    )
  );

const renderBrowser = () => {
  const summarySource =
    liveSnapshot && liveSnapshot.browserSummary && !liveSnapshot.browserSummary.denied
      ? liveSnapshot.browserSummary.counts || dashboardData.browserSummary
      : dashboardData.browserSummary;

  const metrics = (Array.isArray(summarySource) ? summarySource : dashboardData.browserSummary)
    .map(
      (item) => `
        <article class="metric-card" data-tone="${item.tone || statusTone(item.state)}">
          <div>
            <p>${item.label}</p>
            <strong>${item.value}</strong>
          </div>
          ${badge(item.state)}
          <span>Source: ${item.source}</span>
        </article>
      `
    )
    .join('');

  const liveInstances =
    liveSnapshot && Array.isArray(liveSnapshot.browserInstances)
      ? liveSnapshot.browserInstances.map((row) => ({
          instance: row.instance_id || row.instance || '—',
          context: row.context_id || row.context || '—',
          provider: row.provider || row.provider_id || '—',
          identity: row.identity || row.identity_id || '—',
          tab: row.tab_id || row.tab || '—',
          state: row.state || 'unknown',
          job: row.job_id || row.job || '—',
          // Redaction: only ever expose the boolean indicator, never a storage_state URI.
          storage: row.has_storage_state ? 'Snapshot present' : 'No snapshot yet',
          engine: row.engine || '—'
        }))
      : null;

  return panelFrame(
    'browser',
    liveInstances ? 'Browser topology (Live)' : 'Browser topology',
    'Browser → provider context → channel tab hierarchy with tab state badges. Storage state is shown as a boolean snapshot indicator only — never a URI.',
    `
      <div class="metric-grid">${metrics}</div>
      ${table(
        [
          { key: 'instance', label: 'Browser instance' },
          { key: 'context', label: 'Provider context' },
          { key: 'provider', label: 'Provider' },
          { key: 'identity', label: 'Identity' },
          { key: 'tab', label: 'Channel tab' },
          { key: 'state', label: 'State' },
          { key: 'job', label: 'Job' },
          { key: 'storage', label: 'Storage state' },
          { key: 'engine', label: 'Engine' }
        ],
        liveInstances || dashboardData.browserInstances
      )}
      ${renderLiveViewer()}
    `
  );
};

// noVNC live-browser viewer. The dashboard never fills credentials or solves
// CAPTCHAs — "Take control" simply embeds the password-gated remote display so a
// human operator can complete login/2FA in the user-owned session. The viewer is
// loaded only on demand and points at the same-origin edge route (/novnc/),
// which Caddy proxies to the browser-viewer service when the "live-browser"
// compose profile is running.
const NOVNC_VIEWER_SRC =
  '/novnc/vnc.html?autoconnect=true&resize=scale&reconnect=true&path=novnc/websockify';

const renderLiveViewer = () => `
  <section class="live-viewer" aria-labelledby="live-viewer-title" data-novnc-region>
    <div class="list-heading">
      <h3 id="live-viewer-title">Live login viewer (noVNC)</h3>
      <span>Human-in-the-loop · password-gated</span>
    </div>
    <p class="live-viewer-note">
      Take control opens the real, persistent browser running on the worker host.
      A human completes login, CAPTCHA, 2FA, and consent manually — UBAG never
      captures credentials, cookies, or storage state, and never solves
      challenges. Requires the <code>live-browser</code> deployment profile.
    </p>
    <div class="live-viewer-actions">
      <button class="control-button" type="button" data-novnc-action="take-control">
        Take control
      </button>
      <a
        class="control-button secondary"
        data-novnc-action="open-tab"
        href="${NOVNC_VIEWER_SRC}"
        target="_blank"
        rel="noopener noreferrer"
      >Open full screen</a>
      <button
        class="control-button secondary"
        type="button"
        data-novnc-action="release"
        hidden
      >Release / hide</button>
    </div>
    <div class="live-viewer-stage" data-novnc-stage hidden>
      <iframe
        title="Live browser session (noVNC)"
        class="live-viewer-frame"
        data-novnc-frame
        referrerpolicy="no-referrer"
        sandbox="allow-scripts allow-same-origin allow-forms"
      ></iframe>
    </div>
  </section>
`;

const renderConcurrency = () => {
  const liveRows =
    liveSnapshot && Array.isArray(liveSnapshot.concurrency)
      ? liveSnapshot.concurrency.map((row) => ({
          provider: row.provider || row.provider_id || '—',
          identity: row.identity || row.identity_id || '—',
          cap: row.current_cap != null ? String(row.current_cap) : '—',
          bounds:
            row.min_tabs != null && row.max_tabs != null
              ? `${row.min_tabs} / ${row.max_tabs}`
              : '—',
          inflight: row.inflight != null ? String(row.inflight) : '—',
          lastChange: row.last_change_reason || row.last_change || '—',
          state: row.state || 'Ready'
        }))
      : null;

  return panelFrame(
    'concurrency',
    liveRows ? 'Adaptive concurrency (Live)' : 'Adaptive concurrency',
    'AIMD ceilings per provider and identity: current cap, min/max bounds, in-flight tabs, and the reason for the last cap change. Caps cut on CAPTCHA, slow-down banners, or 429s to stay ToS-safe.',
    table(
      [
        { key: 'provider', label: 'Provider' },
        { key: 'identity', label: 'Identity' },
        { key: 'cap', label: 'Current cap' },
        { key: 'bounds', label: 'Min / Max' },
        { key: 'inflight', label: 'In-flight' },
        { key: 'lastChange', label: 'Last change' },
        { key: 'state', label: 'State' }
      ],
      liveRows || dashboardData.concurrency
    )
  );
};

const renderAlerts = () => {
  const liveAlerts =
    liveSnapshot && Array.isArray(liveSnapshot.alerts)
      ? liveSnapshot.alerts.map((row) => ({
          id: row.alert_id || row.id || '—',
          kind: row.kind || 'manual_action',
          job: row.job_id || row.job || '—',
          target: row.target || '—',
          age: row.age || row.age_human || '—',
          status: row.status || 'open',
          detail: row.detail || row.message || ''
        }))
      : null;

  const alerts = liveAlerts || dashboardData.alerts;

  const queue = alerts.length
    ? `<ul class="work-list" aria-label="Open manual-action alerts">${alerts
        .map(
          (item) => `
        <li class="work-item" data-alert-id="${item.id}">
          <div>
            <h3>${item.kind === 'captcha' ? 'CAPTCHA — human solve required' : 'Manual login required'}</h3>
            <p>${item.target} · job ${item.job} · ${item.age} old</p>
            <p>${item.detail}</p>
          </div>
          <div class="alert-actions">
            ${badge(item.status)}
            <button
              class="control-button secondary"
              type="button"
              data-alert-action="acknowledge"
              data-alert-id="${item.id}"
              ${item.status !== 'open' ? 'disabled' : ''}
            >Acknowledge</button>
            <button
              class="control-button"
              type="button"
              data-alert-action="resolve"
              data-alert-id="${item.id}"
              ${item.status === 'resolved' ? 'disabled' : ''}
            >Resolve</button>
          </div>
        </li>
      `
        )
        .join('')}</ul>`
    : emptyState(
        'No open manual-action alerts',
        'CAPTCHA and manual-login prompts appear here for a human operator to resolve via noVNC. UBAG never solves CAPTCHAs automatically.'
      );

  const configRows =
    liveSnapshot && liveSnapshot.alertConfig && !liveSnapshot.alertConfig.denied
      ? [
          { setting: 'Sink', value: liveSnapshot.alertConfig.sink || '—' },
          {
            setting: 'SMTP configured',
            // Redaction: render a yes/no flag, never any password or SMTP credential.
            value: liveSnapshot.alertConfig.smtp_configured ? 'yes' : 'no'
          },
          { setting: 'Default recipient', value: liveSnapshot.alertConfig.recipient || '—' },
          {
            setting: 'Resolution',
            value: 'Human-solved (noVNC takeover) — no automated CAPTCHA bypass'
          }
        ]
      : dashboardData.alertConfig;

  return panelFrame(
    'alerts',
    'Manual-action alerts',
    'Human-in-the-loop queue for CAPTCHA and manual-login prompts. Operators are emailed so a human solves the challenge in the live session — the machine never bypasses it.',
    `
      ${queue}
      <section class="list-panel" aria-labelledby="alert-config-title">
        <div class="list-heading">
          <h3 id="alert-config-title">Alert routing</h3>
          <span>SMTP status (no secrets shown)</span>
        </div>
        ${table(
          [
            { key: 'setting', label: 'Setting' },
            { key: 'value', label: 'Value' }
          ],
          configRows
        )}
      </section>
    `
  );
};

const renderers = {
  overview: renderOverview,
  apps: renderApps,
  targets: renderTargets,
  jobs: renderJobs,
  sessions: renderSessions,
  browser: renderBrowser,
  concurrency: renderConcurrency,
  alerts: renderAlerts,
  templates: renderTemplates,
  runtime: renderRuntime,
  activation: renderActivation
};

const renderPanels = () => {
  panelStack.innerHTML = dashboardTabs.map((tab) => renderers[tab]()).join('');
};

// Delegated handler for human-in-the-loop alert actions. The dashboard only
// records that a human acted; it never solves CAPTCHAs or logs in for the user.
panelStack.addEventListener('click', async (event) => {
  const button = event.target.closest('[data-alert-action]');
  if (!button) return;

  const action = button.dataset.alertAction;
  const alertId = button.dataset.alertId;
  button.setAttribute('aria-busy', 'true');

  if (config.isConfigured) {
    const result =
      action === 'acknowledge' ? await acknowledgeAlert(alertId) : await resolveAlert(alertId);
    if (liveSnapshot && Array.isArray(liveSnapshot.alerts)) {
      const target = liveSnapshot.alerts.find((item) => (item.alert_id || item.id) === alertId);
      if (target) target.status = action === 'acknowledge' ? 'acknowledged' : 'resolved';
    }
    refreshStatus.textContent =
      result.status >= 200 && result.status < 300
        ? `Alert ${alertId} ${action === 'acknowledge' ? 'acknowledged' : 'resolved'} — a human is handling it.`
        : `Alert ${action} request returned status ${result.status}.`;
  } else {
    const target = dashboardData.alerts.find((item) => item.id === alertId);
    if (target) target.status = action === 'acknowledge' ? 'acknowledged' : 'resolved';
    refreshStatus.textContent = `Mock alert ${alertId} ${action === 'acknowledge' ? 'acknowledged' : 'resolved'}.`;
  }

  renderPanels();
  setActiveTab('alerts');
});

// noVNC live-viewer controls. "Take control" lazily loads the embedded viewer
// (so the iframe never connects until a human asks for it); "Release" tears it
// down. The dashboard only frames the password-gated remote display — it never
// transmits credentials or automates the login.
panelStack.addEventListener('click', (event) => {
  const trigger = event.target.closest('[data-novnc-action]');
  if (!trigger) return;

  const action = trigger.dataset.novncAction;
  if (action === 'open-tab') return; // native anchor handles it

  const region = trigger.closest('[data-novnc-region]');
  if (!region) return;
  const stage = region.querySelector('[data-novnc-stage]');
  const frame = region.querySelector('[data-novnc-frame]');
  const releaseButton = region.querySelector('[data-novnc-action="release"]');

  if (action === 'take-control') {
    event.preventDefault();
    if (!frame.getAttribute('src')) {
      frame.setAttribute('src', NOVNC_VIEWER_SRC);
    }
    stage.hidden = false;
    if (releaseButton) releaseButton.hidden = false;
    refreshStatus.textContent =
      'Live viewer requested. A human operator completes login manually — UBAG never fills credentials or solves CAPTCHAs.';
  }

  if (action === 'release') {
    event.preventDefault();
    frame.removeAttribute('src');
    stage.hidden = true;
    releaseButton.hidden = true;
    refreshStatus.textContent = 'Live viewer released.';
  }
});

const setActiveTab = (nextTab, focusPanel = false) => {
  tabButtons.forEach((button) => {
    const isSelected = button.dataset.tab === nextTab;

    button.setAttribute('aria-selected', String(isSelected));
    button.tabIndex = isSelected ? 0 : -1;
  });

  dashboardTabs.forEach((tab) => {
    const panel = document.querySelector(`#panel-${tab}`);
    panel.hidden = tab !== nextTab;
  });

  if (focusPanel) {
    document.querySelector(`#panel-${nextTab}`).focus({ preventScroll: true });
  }
};

const moveTabFocus = (currentIndex, direction) => {
  const nextIndex =
    (currentIndex + direction + tabButtons.length) % tabButtons.length;
  tabButtons[nextIndex].focus();
};

tabButtons.forEach((button, index) => {
  button.addEventListener('click', () => setActiveTab(button.dataset.tab, true));
  button.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowRight') {
      event.preventDefault();
      moveTabFocus(index, 1);
    }

    if (event.key === 'ArrowLeft') {
      event.preventDefault();
      moveTabFocus(index, -1);
    }

    if (event.key === 'Home') {
      event.preventDefault();
      tabButtons[0].focus();
    }

    if (event.key === 'End') {
      event.preventDefault();
      tabButtons[tabButtons.length - 1].focus();
    }

    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      setActiveTab(button.dataset.tab, true);
    }
  });
});

document
  .querySelector('[data-state="success"]')
  .addEventListener('click', async (event) => {
    const button = event.currentTarget;
    button.setAttribute('aria-busy', 'true');
    button.textContent = 'Refreshing';

    if (config.isConfigured) {
      refreshStatus.textContent = 'Fetching live data from gateway…';
      liveSnapshot = await fetchDashboardSnapshot();
      renderPanels();
      if (liveSnapshot.health) {
        statusTitle.textContent = `Gateway: ${liveSnapshot.health.status} (${liveSnapshot.health.version || 'unknown'})`;
        lastSync.textContent = `Live sync ${liveSnapshot.fetchedAt}`;
        refreshStatus.textContent = `Live gateway data refreshed at ${liveSnapshot.fetchedAt}.`;
      } else {
        refreshStatus.textContent = 'Gateway unreachable — still showing last known data.';
      }
    } else {
      refreshStatus.textContent = 'Refreshing local mock dashboard data.';
    }

    window.setTimeout(() => {
      button.removeAttribute('aria-busy');
      button.textContent = config.isConfigured ? 'Refresh live data' : 'Refresh mock data';
      button.classList.add('is-success');

      window.setTimeout(() => {
        button.textContent = config.isConfigured ? 'Refresh live data' : 'Refresh mock data';
        button.classList.remove('is-success');
      }, 1600);
    }, 450);
  });

// --- Settings dialog ---
const settingsDialog = document.querySelector('#settings-dialog');
const cfgUrl = document.querySelector('#cfg-gateway-url');
const cfgSecret = document.querySelector('#cfg-app-secret');

document.querySelector('#btn-settings').addEventListener('click', () => {
  cfgUrl.value = localStorage.getItem('ubag_gateway_url') || 'http://127.0.0.1:8081';
  cfgSecret.value = localStorage.getItem('ubag_app_secret') || '';
  settingsDialog.showModal();
});

document.querySelector('#btn-cancel-settings').addEventListener('click', () => {
  settingsDialog.close();
});

document.querySelector('#btn-connect-gateway').addEventListener('click', () => {
  if (config.isConfigured) {
    // already connected — just refresh
    document.querySelector('[data-state="success"]').click();
  } else {
    cfgUrl.value = 'http://127.0.0.1:8081';
    cfgSecret.value = '';
    settingsDialog.showModal();
  }
});

settingsDialog.addEventListener('close', () => {
  if (settingsDialog.returnValue === 'default' || !cfgUrl.value || !cfgSecret.value) return;
});

document.querySelector('.settings-form').addEventListener('submit', (e) => {
  e.preventDefault();
  if (cfgUrl.value && cfgSecret.value) {
    saveConfig(cfgUrl.value, cfgSecret.value);
  }
});

renderPanels();
