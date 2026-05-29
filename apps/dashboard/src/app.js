import { dashboardData, dashboardTabs } from './mock-data.js';
import { getConfig, saveConfig, fetchDashboardSnapshot, createJob, cancelJob } from './gateway-client.js';

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
  sourcePill.textContent = 'Local mock data';
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
    'Not connected': 'muted'
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

const renderers = {
  overview: renderOverview,
  apps: renderApps,
  targets: renderTargets,
  jobs: renderJobs,
  sessions: renderSessions,
  templates: renderTemplates,
  runtime: renderRuntime,
  activation: renderActivation
};

const renderPanels = () => {
  panelStack.innerHTML = dashboardTabs.map((tab) => renderers[tab]()).join('');
};

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
