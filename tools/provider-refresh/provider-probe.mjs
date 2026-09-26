// Read-only live-DOM probe for UBAG provider adapters.
//
// Companion to tools/live-browser/bridge.mjs (which streams video/input only).
// This tool answers the maintenance question the bridge cannot: "does the
// provider's CURRENT web UI still match what adapters/<id>/manifest.json
// declares and apps/worker/ubag_worker/live/selectors.py clicks?"
//
// What it does per provider:
//   1. Finds the provider's tab in the automation Chrome over CDP (or opens a
//      new one at the manifest's target_homepage — opening a tab is the only
//      state change it ever makes).
//   2. Evaluates a read-only extraction in the page: composer candidates,
//      visible buttons/menu items/switches with labels + aria states, and a
//      login-wall heuristic.
//   3. Checks every pure-CSS candidate of every selectors.py SelectorGroup
//      against the live DOM (playwright-only candidates are reported as such,
//      not silently "unmatched").
//   4. Compares the manifest's model_catalog choice values against the labels
//      seen (current-value detection works with closed menus; full menu
//      contents need --open-menus).
//   5. Writes tools/provider-refresh/captures/<id>-<UTC timestamp>.json and
//      prints a human summary.
//
// HARD SAFETY RULES (mirrors the manifests' safe_mode policy):
//   - Never types text into a provider page, never submits a prompt.
//   - Never logs in; a login wall is reported, not bypassed.
//   - Without --open-menus it performs zero clicks.
//   - With --open-menus it only toggles [aria-haspopup] menu buttons open and
//      re-closes them (the same menus every live job opens anyway), then
//      Escape-dismisses. No other interaction.
//
// Usage:
//   node tools/provider-refresh/provider-probe.mjs [provider_id ...] [--open-menus] [--cdp URL]
// Provider ids default to every live web target with a manifest homepage.
// CDP endpoint: --cdp or env UBAG_PROBE_CDP (default http://127.0.0.1:15923 —
// the SSH-tunnelled production Chrome of the local deployment).

import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import {
  LIVE_TARGET_IDS,
  isPureCssSelector,
  listAdapterManifests,
  liveProvidersBySelectorId,
  repoPath,
  repoRoot,
} from './lib.mjs';

const args = process.argv.slice(2);
const openMenus = args.includes('--open-menus');
const cdpIdx = args.indexOf('--cdp');
const cdpBase = (cdpIdx >= 0 ? args[cdpIdx + 1] : null) || process.env.UBAG_PROBE_CDP || 'http://127.0.0.1:15923';
const wanted = args.filter((a) => !a.startsWith('--'));
const captureDir = repoPath('tools', 'provider-refresh', 'captures');

const manifests = listAdapterManifests().filter(
  (a) => a.manifest && (a.manifest.target_homepage || LIVE_TARGET_IDS.includes(a.id)),
);
const selectorProviders = liveProvidersBySelectorId();

const targets = (wanted.length ? wanted : LIVE_TARGET_IDS).map((id) => {
  const entry = manifests.find((a) => a.id === id);
  const selectors = selectorProviders[id];
  if (!entry || !entry.manifest) {
    return { id, error: `no manifest for ${id}` };
  }
  if (!selectors) {
    return { id, error: `no selectors.py block for ${id}` };
  }
  return { id, manifest: entry.manifest, selectors };
});

for (const target of targets) {
  if (target.error) {
    console.error(`✗ ${target.id}: ${target.error}`);
    process.exitCode = 2;
  }
}
if (targets.every((t) => t.error)) process.exit(2);

// --- minimal CDP client (same shape as bridge.mjs's PageSession) -----------
let wsSeq = 0;
const pending = new Map();

function cdpConnect(wsUrl) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(wsUrl);
    ws.onopen = () => resolve(ws);
    ws.onerror = () => reject(new Error(`CDP websocket failed: ${wsUrl}`));
    ws.onclose = () => {
      for (const p of pending.values()) p.reject(new Error('CDP connection closed'));
      pending.clear();
    };
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && pending.has(msg.id)) {
        const p = pending.get(msg.id);
        pending.delete(msg.id);
        if (msg.error) p.reject(new Error(msg.error.message || JSON.stringify(msg.error)));
        else p.resolve(msg.result);
      }
    };
  });
}

function cdpSend(ws, method, params = {}, timeoutMs = 15000) {
  const id = ++wsSeq;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
    setTimeout(() => {
      if (pending.has(id)) {
        pending.delete(id);
        reject(new Error(`CDP ${method} timed out`));
      }
    }, timeoutMs);
  });
}

async function httpJson(base, pathname, method = 'GET') {
  let res;
  try {
    res = await fetch(base + pathname, { method });
  } catch (err) {
    throw new Error(
      `CDP endpoint unreachable at ${base} (${err.cause?.code ?? err.message}) — `
      + `is the SSH tunnel up and the production Chrome running? start-local.ps1 `
      + `starts tunnel-watchdog.ps1, which self-heals the tunnel every 30s.`,
    );
  }
  if (!res.ok) throw new Error(`${method} ${pathname} -> ${res.status}`);
  return res.json();
}

// --- in-page extraction (READ-ONLY) ----------------------------------------
const EXTRACT_FN = `(() => {
  const vis = (el) => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
  const label = (el) => (el.getAttribute('aria-label') || el.textContent || '')
    .trim().replace(/\\s+/g, ' ').slice(0, 90) || undefined;
  const controlSel = "button, a[href], [role='menuitem'], [role='menuitemradio'], [role='menuitemcheckbox'], [role='switch'], [role='combobox'], [aria-haspopup], [data-testid]";
  const controls = [...document.querySelectorAll(controlSel)].filter(vis).slice(0, 300).map((el) => ({
    tag: el.tagName.toLowerCase(),
    role: el.getAttribute('role') || undefined,
    label: label(el),
    checked: el.getAttribute('aria-checked') || undefined,
    expanded: el.getAttribute('aria-expanded') || undefined,
    haspopup: el.getAttribute('aria-haspopup') || undefined,
    testid: el.getAttribute('data-testid') || undefined,
  }));
  const composerSel = "textarea, div[contenteditable='true'], rich-textarea";
  const composers = [...document.querySelectorAll(composerSel)].filter(vis).slice(0, 6).map((el) => ({
    tag: el.tagName.toLowerCase(),
    placeholder: el.getAttribute('placeholder') || el.getAttribute('aria-label')
      || el.getAttribute('data-placeholder') || undefined,
    testid: el.getAttribute('data-testid') || undefined,
  }));
  const loginish = [...document.querySelectorAll("button, a")]
    .filter(vis).map(label).filter((t) => t && /log ?in|sign ?up|continue with/i.test(t)).slice(0, 8);
  // Provider pickers increasingly are plain DIVs (no button role, no testid).
  // Dump everything interactive-looking inside the composer's container so a
  // rebaseline can see them regardless of element type.
  const composer0 = [...document.querySelectorAll(composerSel)].filter(vis)[0];
  let neighborhood = [];
  if (composer0) {
    let box = composer0;
    for (let i = 0; i < 4 && box.parentElement; i++) {
      if (box.closest('form')) { box = box.closest('form'); break; }
      box = box.parentElement;
    }
    neighborhood = [...box.querySelectorAll('*')]
      .filter((el) => vis(el) && (el.children.length === 0 || el.hasAttribute('aria-label') || el.hasAttribute('role') || el.hasAttribute('data-testid')))
      .filter((el) => el.hasAttribute('aria-label') || el.hasAttribute('role') || el.hasAttribute('data-testid')
        || getComputedStyle(el).cursor === 'pointer')
      .slice(0, 120)
      .map((el) => ({
        tag: el.tagName.toLowerCase(),
        role: el.getAttribute('role') || undefined,
        label: label(el),
        testid: el.getAttribute('data-testid') || undefined,
        aria: [...el.attributes].filter((a) => a.name.startsWith('aria-')).reduce((m, a) => (m[a.name] = a.value, m), {}),
        cursor: getComputedStyle(el).cursor === 'pointer' || undefined,
      }));
  }
  return JSON.stringify({
    url: location.href,
    title: document.title,
    ready: document.readyState,
    composers,
    neighborhood,
    controls,
    loginish,
  });
})()`;

const checkSelectorsExpr = (candidates) => `((candidates) => JSON.stringify(candidates.map((sel) => {
  try { return { sel, count: document.querySelectorAll(sel).length }; }
  catch { return { sel, error: true }; }
})))(${JSON.stringify(candidates)})`;

async function evaluateJson(ws, expression) {
  // Runtime.evaluate takes an EXPRESSION (arguments are callFunctionOn-only),
  // so callers embed their data as JSON literals in the expression string.
  const params = { expression, returnByValue: true, awaitPromise: false };
  const res = await cdpSend(ws, 'Runtime.evaluate', params);
  if (res.exceptionDetails) throw new Error('page evaluate failed: ' + JSON.stringify(res.exceptionDetails).slice(0, 300));
  return JSON.parse(res.result.value);
}

async function waitForReady(ws, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const state = await cdpSend(ws, 'Runtime.evaluate', {
        expression: 'document.readyState',
        returnByValue: true,
      });
      if (state.result.value === 'complete') return;
    } catch {
      /* page may be mid-navigation; retry */
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

const MENU_LABEL_CAP = 120;

async function captureOnce(ws) {
  return evaluateJson(ws, EXTRACT_FN);
}

async function openMenuAndCapture(ws, control) {
  // Click the menu-open control by matching its identifying attributes in
  // page (never by coordinates), capture the menu items, then re-close.
  // React menus usually listen to pointer events, not the synthetic click();
  // fire the full pointer sequence (still only ON the menu-open button).
  const opener = `(() => {
    const needle = ${JSON.stringify({ testid: control.testid ?? null, label: control.label ?? '' })};
    const els = [...document.querySelectorAll("[aria-haspopup]")].filter((el) => el.offsetWidth || el.offsetHeight);
    const el = els.find((e) => (needle.testid && (e.getAttribute('data-testid') || '') === needle.testid)
      || (!needle.testid && (e.getAttribute('aria-label') || e.textContent || '').trim().startsWith((needle.label || '').slice(0, 20))));
    if (!el) return false;
    const r = el.getBoundingClientRect();
    const opts = { bubbles: true, cancelable: true, clientX: r.x + r.width / 2, clientY: r.y + r.height / 2, button: 0 };
    for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) {
      el.dispatchEvent(type.startsWith('pointer')
        ? new PointerEvent(type, { ...opts, pointerId: 1, pointerType: 'mouse', isPrimary: true })
        : new MouseEvent(type, opts));
    }
    return true;
  })()`;
  const closer = `() => {
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    const open = [...document.querySelectorAll("[aria-haspopup][aria-expanded='true']")][0];
    if (open) { open.click(); return 'toggled'; }
    return 'escape';
  }`;
  await cdpSend(ws, 'Runtime.evaluate', { expression: opener, returnByValue: true });
  await new Promise((r) => setTimeout(r, 400));
  let items = [];
  try {
    // Capture menu roles AND a generic labelled-control dump — custom
    // dropdowns (duck.ai's included) often render without menu roles.
    const dump = await evaluateJson(ws, `(() => {
      const vis = (el) => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
      const label = (el) => (el.getAttribute('aria-label') || el.textContent || '')
        .trim().replace(/\\s+/g, ' ').slice(0, 90) || undefined;
      const roles = [...document.querySelectorAll("[role='menuitem'], [role='menuitemradio'], [role='menuitemcheckbox'], [role='option']")]
        .filter(vis).slice(0, ${MENU_LABEL_CAP}).map((el) => ({
          role: el.getAttribute('role'),
          label: label(el),
          checked: el.getAttribute('aria-checked') || undefined,
        }));
      const generic = [...document.querySelectorAll("button, [role], [data-testid]")]
        .filter(vis).slice(0, ${MENU_LABEL_CAP}).map((el) => ({
          role: el.getAttribute('role') || el.tagName.toLowerCase(),
          label: label(el),
          checked: el.getAttribute('aria-checked') || undefined,
          testid: el.getAttribute('data-testid') || undefined,
        }));
      return JSON.stringify({ roles, generic });
    })()`);
    items = dump;
  } finally {
    await cdpSend(ws, 'Runtime.evaluate', { expression: closer, returnByValue: true });
    await new Promise((r) => setTimeout(r, 250));
  }
  return items;
}

/**
 * Attach to a tab and verify it actually responds. Chrome freezes/discards
 * backgrounded tabs (memory saver): the websocket accepts and then every
 * Runtime command hangs forever — observed live when the first probe run
 * attached to a stale duck.ai tab. So: short-timeout ping before use, and
 * Runtime.evaluate does not require Runtime.enable for evaluate-only work.
 */
async function attachResponsive(tab) {
  let ws;
  try {
    ws = await cdpConnect(tab.webSocketDebuggerUrl);
  } catch {
    return null;
  }
  try {
    await cdpSend(ws, 'Runtime.evaluate', { expression: '1+1', returnByValue: true }, 4000);
    return ws;
  } catch {
    try { ws.close(); } catch { /* ignore */ }
    return null;
  }
}

async function openFreshTab(homepage) {
  return httpJson(cdpBase, `/json/new?${encodeURIComponent(homepage)}`, 'PUT')
    .catch(() => httpJson(cdpBase, `/json/new?${encodeURIComponent(homepage)}`));
}

function hostOf(url) {
  try {
    return new URL(url).host;
  } catch {
    return null;
  }
}

async function probeProvider(target) {
  const { id, manifest, selectors } = target;
  const homepage = manifest.target_homepage;
  const host = hostOf(homepage);
  const list = await httpJson(cdpBase, '/json/list');
  let openedTab = false;
  let ws = null;
  const existing = list.find(
    (t) => t.type === 'page' && t.url && host && hostOf(t.url) === host,
  );
  if (existing) {
    ws = await attachResponsive(existing);
  }
  if (!ws) {
    // No matching tab, or a frozen one that ignores CDP: open a fresh tab at
    // the provider homepage (the only state change this tool makes).
    const tab = await openFreshTab(homepage);
    openedTab = true;
    ws = await attachResponsive(tab);
    if (!ws) throw new Error('freshly opened tab is not responding to CDP');
  }
  try {
    await waitForReady(ws);
    const page = await captureOnce(ws);
    // Pure-CSS selector checks against the live DOM.
    const selectorChecks = {};
    for (const [groupName, group] of Object.entries(selectors.groups)) {
      const cssCandidates = group.candidates.filter(isPureCssSelector);
      const playwrightOnly = group.candidates.filter((s) => !isPureCssSelector(s));
      let results = [];
      if (cssCandidates.length) {
        results = await evaluateJson(ws, checkSelectorsExpr(cssCandidates));
      }
      selectorChecks[groupName] = {
        matched: results.some((r) => (r.count ?? 0) > 0),
        css: results,
        playwrightOnly,
      };
    }
    // Menu captures (explicit opt-in — see the safety rules in the header).
    const menuCaptures = {};
    if (openMenus) {
      // Provider-specific pickers carry data-testids; anonymous "menu" buttons
      // (sidebar rows etc.) are noise — testid-bearing controls go first.
      const relevant = (c) => /model|think|reason|effort|mode/i.test(c.label || '');
      const popupControls = page.controls.filter(
        (c) => c.haspopup && (c.testid || (c.label && c.label !== 'menu') || relevant(c)),
      );
      popupControls.sort((a, b) =>
        ((b.testid ? 2 : 0) + (relevant(b) ? 1 : 0)) - ((a.testid ? 2 : 0) + (relevant(a) ? 1 : 0)));
      for (const control of popupControls.slice(0, 10)) {
        const key = control.testid || control.label || 'control';
        try {
          menuCaptures[key] = await openMenuAndCapture(ws, control);
        } catch (err) {
          menuCaptures[key] = { error: String(err).slice(0, 200) };
        }
      }
    }
    // Manifest comparison: are the declared choice values visible anywhere?
    const settings = manifest.model_catalog?.settings ?? {};
    const catalogCompare = {};
    const allLabels = [
      ...page.controls.map((c) => c.label),
      ...Object.values(menuCaptures).flatMap((m) => {
        const roles = Array.isArray(m) ? m : (m.roles ?? []);
        return roles.map((i) => i.label);
      }),
    ].filter(Boolean);
    for (const [key, setting] of Object.entries(settings)) {
      if (setting.kind !== 'choice') {
        catalogCompare[key] = { kind: 'toggle', note: 'toggle state not verifiable read-only' };
        continue;
      }
      const values = setting.values ?? [];
      catalogCompare[key] = {
        kind: 'choice',
        values: values.map((v) => ({
          value: v,
          seen: allLabels.some((l) => l.toLowerCase().includes(v.toLowerCase())),
        })),
      };
    }
    const likelyLoginWall =
      page.loginish.length > 0 && page.composers.length === 0;
    return {
      id,
      captured_at: new Date().toISOString(),
      cdp: cdpBase,
      opened_tab: openedTab,
      url: page.url,
      title: page.title,
      likely_login_wall: likelyLoginWall,
      loginish: page.loginish,
      composers: page.composers,
      composer_neighborhood: page.neighborhood,
      manifest_catalog_compare: catalogCompare,
      menu_captures: openMenus ? menuCaptures : undefined,
      selector_checks: selectorChecks,
      desired_defaults: Object.fromEntries(
        Object.entries(selectors.settings).map(([k, s]) => [k, s.desired]),
      ),
      raw_controls: page.controls,
    };
  } finally {
    try { ws.close(); } catch { /* ignore */ }
  }
}

mkdirSync(captureDir, { recursive: true });
const reports = [];
for (const target of targets) {
  if (target.error) continue;
  process.stdout.write(`probing ${target.id} ... `);
  try {
    const report = await probeProvider(target);
    const stamp = report.captured_at.replace(/[:.]/g, '-').slice(0, 19);
    const file = path.join(captureDir, `${target.id}-${stamp}.json`);
    writeFileSync(file, JSON.stringify(report, null, 2) + '\n');
    reports.push({ report, file });
    const wall = report.likely_login_wall ? ' [LOGIN WALL — verify after human sign-in]' : '';
    console.log(`ok -> ${path.relative(repoRoot, file)}${wall}`);
  } catch (err) {
    console.log(`FAILED: ${String(err).slice(0, 300)}`);
    process.exitCode = 2;
  }
}

// Human summary.
for (const { report } of reports) {
  console.log(`\n=== ${report.id} (${report.url}) ===`);
  if (report.likely_login_wall) console.log('  ⚠ login wall detected — composer hidden');
  for (const [key, cmp] of Object.entries(report.manifest_catalog_compare)) {
    if (cmp.kind !== 'choice') continue;
    const missing = cmp.values.filter((v) => !v.seen).map((v) => v.value);
    if (missing.length) {
      console.log(`  ${key}: not visible (menus closed or drifted): ${missing.join(', ')}`);
    } else {
      console.log(`  ${key}: all ${cmp.values.length} declared values seen`);
    }
  }
  for (const [group, check] of Object.entries(report.selector_checks)) {
    const matched = check.matched ? 'match' : 'NO MATCH';
    const pw = check.playwrightOnly.length ? ` (+${check.playwrightOnly.length} playwright-only)` : '';
    console.log(`  ${group}: ${matched}${pw}`);
  }
}
console.log(
  `\n${reports.length} captured. Re-run with --open-menus to enumerate closed menus.`,
);
