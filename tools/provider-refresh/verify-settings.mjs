// Live canary for provider settings: replays the worker engine's
// ensure_provider_config flow (open menu -> satisfied? -> apply -> dismiss ->
// re-open -> satisfied?) against the REAL logged-in provider UIs over CDP.
//
// This is the "live canary" step of the drift repair loop (see
// apps/docs/src/content/docs/adapters/drift-detection.md) for selector
// rebases: it proves the NEW open_steps / satisfied_when / apply_click
// templates in apps/worker/ubag_worker/live/selectors.py actually work in the
// current provider DOM, without submitting any prompt.
//
// Safety: interacts ONLY with provider model/effort menus (the same menus
// every live job opens); never types text, never submits, never logs in.
// Settings are left at their operator defaults (which is the enforced state
// every job applies anyway).
//
// Usage: node tools/provider-refresh/verify-settings.mjs [provider_id ...]
// CDP: --cdp URL or UBAG_PROBE_CDP (default http://127.0.0.1:15923).

import { listAdapterManifests, liveProvidersBySelectorId, repoRoot } from './lib.mjs';
import path from 'node:path';

const args = process.argv.slice(2);
const cdpIdx = args.indexOf('--cdp');
const cdpBase = (cdpIdx >= 0 ? args[cdpIdx + 1] : null) || process.env.UBAG_PROBE_CDP || 'http://127.0.0.1:15923';
const wanted = args.filter((a) => !a.startsWith('--'));

const manifests = Object.fromEntries(
  listAdapterManifests().map((a) => [a.id, a.manifest]),
);
const selectorsByProvider = liveProvidersBySelectorId();
const targets = (wanted.length ? wanted : Object.keys(selectorsByProvider)).filter(
  (id) => selectorsByProvider[id] && manifests[id]?.target_homepage,
);

// Playwright-style matcher shim: supports pure CSS plus at most one
// :has-text("...") / :has-text('...') clause, and treats `text='x'` /
// `text-is('x')` standalone selectors as text-equality queries. Anything
// richer (xpath=, >>, :near) is reported as unsupported instead of
// silently mismatching.
function matchCountExpr(selector) {
  const hasText = /:has-text\(("|')(.+?)\1\)/.exec(selector);
  const base = selector.replace(/:has-text\(("|')(.+?)\1\)/g, '');
  if (/xpath=|>>|:near\(|text-is\(/.test(selector)) return null;
  const arg = JSON.stringify(hasText ? hasText[2] : null);
  return `(() => {
    const needle = ${arg};
    let els;
    try { els = [...document.querySelectorAll(${JSON.stringify(base)})]; }
    catch { return -1; }
    if (needle === null) return els.length;
    return els.filter((el) => (el.textContent || '').includes(needle)).length;
  })()`;
}

async function attach(tab) {
  const ws = new WebSocket(tab.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let seq = 0; const pend = new Map();
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pend.has(m.id)) {
      const p = pend.get(m.id);
      pend.delete(m.id);
      if (m.error) p.rej(new Error(m.error.message || JSON.stringify(m.error)));
      else p.res(m.result);
    }
  };
  return {
    async eval(expression) {
      const r = await send('Runtime.evaluate', { expression, returnByValue: true });
      if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 200));
      return r.result.value;
    },
    close() { try { ws.close(); } catch { /* ignore */ } },
  };
  function send(method, params) {
    return new Promise((res, rej) => {
      const id = ++seq;
      pend.set(id, { res, rej });
      ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => { if (pend.has(id)) { pend.delete(id); rej(new Error('cdp timeout ' + method)); } }, 12000);
    });
  }
}

const clickFirstExpr = (candidates) => `(() => {
  const candidates = ${JSON.stringify(candidates)};
  for (const sel of candidates) {
    const m = /:has-text\\(("|')(.+?)\\1\\)/.exec(sel);
    const base = sel.replace(/:has-text\\(("|')(.+?)\\1\\)/g, '');
    let els;
    try { els = [...document.querySelectorAll(base)]; } catch { continue; }
    if (m) els = els.filter((el) => (el.textContent || '').includes(m[2]));
    const el = els.find((el) => el.offsetWidth || el.offsetHeight || el.getClientRects().length);
    if (el) {
      const r = el.getBoundingClientRect();
      const o = { bubbles: true, cancelable: true, clientX: r.x + r.width/2, clientY: r.y + r.height/2, button: 0 };
      for (const type of ['pointerdown','mousedown','pointerup','mouseup','click'])
        el.dispatchEvent(new PointerEvent(type, {...o, pointerId:1, pointerType:'mouse', isPrimary:true}));
      return sel;
    }
  }
  return null;
})()`;

const presentExpr = (selector) => {
  const countExpr = matchCountExpr(selector);
  if (countExpr === null) return null;
  return `(() => { const c = ${countExpr}; return c; })()`;
};

const DISMISS = `(() => {
  document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  for (const el of [...document.querySelectorAll("[aria-expanded='true']")].slice(0, 3)) {
    if (el.offsetWidth) { el.click(); }
  }
  return 'ok';
})()`;

async function verifySetting(tab, providerId, setting, manifestValues) {
  const result = { key: setting.key, kind: setting.kind, desired: setting.desired, steps: [] };
  const attempts = [1, 2, 3];
  for (const attempt of attempts) {
    // _open_control: click the first matching candidate of each open step.
    for (const step of setting.open_steps ?? []) {
      const clicked = await tab.eval(clickFirstExpr(step));
      result.steps.push(`attempt${attempt}: open ${clicked ? 'via ' + clicked : 'FAILED (no candidate visible)'}`);
      await new Promise((r) => setTimeout(r, 650));
    }
    // _setting_satisfied
    let satisfied = false;
    let checked = null;
    if (setting.kind === 'toggle') {
      for (const onWhen of setting.on_when ?? []) {
        const expr = presentExpr(onWhen);
        if (expr === null) { result.steps.push(`attempt${attempt}: on_when ${onWhen} (playwright-only, skipped)`); continue; }
        const n = await tab.eval(expr);
        checked = { onWhen, n };
        if (n > 0) { satisfied = true; break; }
      }
      satisfied = satisfied === Boolean(setting.desired);
    } else {
      const expr = presentExpr(String(setting.satisfied_when ?? '').replace('{value}', setting.desired));
      if (expr === null) { result.steps.push(`attempt${attempt}: satisfied_when unsupported (playwright-only)`); }
      else {
        const n = await tab.eval(expr);
        checked = { satisfied_when: setting.satisfied_when, n };
        satisfied = n > 0;
      }
    }
    result.steps.push(`attempt${attempt}: satisfied=${satisfied} (checked ${JSON.stringify(checked)})`);
    if (satisfied) {
      result.state = attempt === 1 ? 'already_set' : 'set';
      return result;
    }
    if (attempt === attempts.length) {
      result.state = 'unverified';
      return result;
    }
    // _apply_setting
    if (setting.kind === 'toggle') {
      for (const onWhen of setting.on_when ?? []) {
        const expr = presentExpr(onWhen);
        if (expr !== null) {
          const n = await tab.eval(expr);
          if (Boolean(n > 0) === Boolean(setting.desired)) { result.steps.push(`attempt${attempt}: already at desired`); break; }
        }
      }
      const clicked = await tab.eval(clickFirstExpr(setting.toggle_click ?? []));
      result.steps.push(`attempt${attempt}: toggle_click ${clicked ? 'via ' + clicked : 'FAILED'}`);
    } else {
      const clicked = await tab.eval(clickFirstExpr([String(setting.apply_click ?? '').replace('{value}', setting.desired)]));
      result.steps.push(`attempt${attempt}: apply_click ${clicked ? 'via ' + clicked : 'FAILED'}`);
    }
    await tab.eval(DISMISS);
    await new Promise((r) => setTimeout(r, 300 * attempt));
  }
  return result;
}

const overall = { started_at: new Date().toISOString(), providers: {} };
let hadFailure = false;
for (const id of targets) {
  const selectors = selectorsByProvider[id];
  const homepage = manifests[id].target_homepage;
  const host = new URL(homepage).host;
  process.stdout.write(`verifying ${id} ... `);
  try {
    const list = await (await fetch(cdpBase + '/json/list')).json();
    const tabInfo = list.find((t) => t.type === 'page' && (t.url || '').includes(host));
    if (!tabInfo) { console.log('no tab open (open it in the live browser first)'); overall.providers[id] = { error: 'no tab' }; hadFailure = true; continue; }
    const tab = await attach(tabInfo);
    try {
      const ready = await tab.eval('document.readyState');
      if (ready !== 'complete') await new Promise((r) => setTimeout(r, 2000));
      // The engine configures a FRESH chat (new chat -> ensure settings); some
      // pickers (chatgpt's effort pill) only exist on the composer screen.
      const newChat = selectors.groups.new_chat?.candidates;
      if (newChat?.length) {
        const clicked = await tab.eval(clickFirstExpr(newChat));
        await new Promise((r) => setTimeout(r, 1200));
        if (!clicked) console.log(`(note: new_chat candidates did not match — verifying on the current screen)`);
      }
      const settings = [];
      for (const [key, s] of Object.entries(selectors.settings)) {
        settings.push({
          key, kind: s.kind, desired: s.desired, required: s.required,
          open_steps: s.open_steps,
          satisfied_when: s.satisfied_when,
          on_when: s.on_when, toggle_click: s.toggle_click, apply_click: s.apply_click,
        });
      }
      const out = [];
      for (const s of settings) {
        out.push(await verifySetting(tab, id, s));
      }
      overall.providers[id] = { settings: out };
      const bad = out.filter((r) => r.state === 'unverified');
      if (!bad.length) console.log('all settings verified');
      else {
        const hardFail = bad.filter((b) => b.required);
        const bestEffort = bad.filter((b) => !b.required);
        if (bestEffort.length) console.log(`best-effort unverified (setting absent in this UI state, non-blocking): ${bestEffort.map((b) => b.key).join(', ')}`);
        if (hardFail.length) { console.log(`UNVERIFIED (required — will fail jobs): ${hardFail.map((b) => b.key).join(', ')}`); hadFailure = true; }
      }
      for (const r of out) {
        for (const step of r.steps) console.log('   ', r.key, '|', step);
      }
    } finally { tab.close(); }
  } catch (err) {
    console.log('FAILED:', String(err).slice(0, 200));
    overall.providers[id] = { error: String(err).slice(0, 300) };
    hadFailure = true;
  }
}
const { writeFileSync, mkdirSync } = await import('node:fs');
mkdirSync(path.join(repoRoot, 'tools', 'provider-refresh', 'captures'), { recursive: true });
const stamp = overall.started_at.replace(/[:.]/g, '-').slice(0, 19);
writeFileSync(
  path.join(repoRoot, 'tools', 'provider-refresh', 'captures', `verify-settings-${stamp}.json`),
  JSON.stringify(overall, null, 2) + '\n',
);
process.exit(hadFailure ? 1 : 0);
