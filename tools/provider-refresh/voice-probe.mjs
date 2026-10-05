// Read-only voice-control probe for UBAG provider pages.
//
// Companion to provider-probe.mjs. Answers the voice-release question:
// "does the provider's CURRENT web UI expose its voice-mode control, and what
// does it look like?" — without clicking anything. Never types, never clicks,
// never logs in, never opens menus.
//
// Usage: node tools/provider-refresh/voice-probe.mjs [provider_id ...] [--cdp URL]

import { writeFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { repoPath, repoRoot } from './lib.mjs';

const args = process.argv.slice(2);
const cdpIdx = args.indexOf('--cdp');
const cdpBase = (cdpIdx >= 0 ? args[cdpIdx + 1] : null) || process.env.UBAG_PROBE_CDP || 'http://127.0.0.1:15923';
const wanted = args.filter((a) => !a.startsWith('--'));
const homes = wanted.length
  ? Object.fromEntries(wanted.map((id) => [id, null]))
  : { chatgpt_web: 'https://chatgpt.com/', gemini_web: 'https://gemini.google.com/app' };

const extraction = () => {
  const kw = /(voice|audio|mic|waveform|sound|dictat|speak|talk|live)/i;
  const describe = (el) => {
    if (!el || el.nodeType !== 1) return null;
    const r = el.getBoundingClientRect();
    return {
      tag: el.tagName.toLowerCase(),
      aria_label: el.getAttribute('aria-label') || null,
      testid: el.getAttribute('data-testid') || el.getAttribute('data-test-id') || null,
      title: el.getAttribute('title') || null,
      text: (el.textContent || '').trim().slice(0, 80),
      visible: r.width > 0 && r.height > 0,
      rect: { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) },
    };
  };
  // 1. Buttons/inputs anywhere on the page whose label matches voice keywords.
  const all = [...document.querySelectorAll('button,[role="button"],input[type="file"],[aria-label]')];
  const voiceish = [];
  const seen = new Set();
  for (const el of all) {
    const d = describe(el);
    if (!d || !d.visible) continue;
    const hay = [d.aria_label, d.testid, d.title, d.text].filter(Boolean).join(' ');
    if (kw.test(hay)) {
      const key = `${d.tag}|${d.aria_label}|${d.testid}|${d.rect.x},${d.rect.y}`;
      if (!seen.has(key)) {
        seen.add(key);
        voiceish.push(d);
      }
    }
  }
  // 2. The composer region: nearest form or main editable area, plus its buttons.
  const composer = document.querySelector('form [contenteditable="true"], form textarea, textarea[enterkeyhint], [contenteditable="true"][role="textbox"]');
  const composerButtons = [];
  if (composer) {
    const region = composer.closest('form') || composer.parentElement?.parentElement || composer.parentElement;
    for (const el of (region?.querySelectorAll('button,[role="button"]') || [])) {
      const d = describe(el);
      if (d && d.visible) composerButtons.push(d);
    }
  }
  return {
    url: location.href,
    title: document.title,
    composer: describe(composer),
    composer_buttons: composerButtons,
    voiceish_controls: voiceish.slice(0, 40),
    media_devices_probe: 'skipped (requires permission gesture; read-only probe never triggers it)',
  };
};

const targets = await (await fetch(`${cdpBase}/json`)).json();
for (const id of Object.keys(homes)) {
  const home = homes[id];
  const tab = targets.find((t) => t.type === 'page' && (home ? (t.url || '').startsWith(home) : t.url?.includes(id))) ||
    targets.find((t) => t.type === 'page' && (id === 'chatgpt_web' ? t.url?.includes('chatgpt.com') : t.url?.includes('gemini.google.com')));
  if (!tab) {
    console.log(`${id}: NO TAB OPEN`);
    continue;
  }
  const ws = new WebSocket(tab.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error(`CDP websocket failed: ${tab.webSocketDebuggerUrl}`)); });
  let seq = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
  };
  const send = (method, params = {}) => new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, (m) => (m.error ? rej(new Error(m.error.message)) : res(m.result)));
    ws.send(JSON.stringify({ id, method, params }));
    setTimeout(() => { if (pending.has(id)) { pending.delete(id); rej(new Error(`CDP ${method} timed out`)); } }, 15000);
  });
  try {
    const { result } = await send('Runtime.evaluate', {
      expression: `(${extraction.toString()})()`,
      returnByValue: true,
      awaitPromise: true,
    });
    const out = { id, captured_at: new Date().toISOString(), url: tab.url, ...result.value };
    const file = repoPath('tools', 'provider-refresh', 'captures', `${id}-voice-${new Date().toISOString().replace(/[:.]/g, '-')}.json`);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, JSON.stringify(out, null, 2));
    console.log(`\n=== ${id} (${tab.url}) -> ${path.relative(repoRoot, file)}`);
    console.log('  composer:', out.composer ? `${out.composer.tag} "${out.composer.aria_label}"` : 'none');
    console.log('  composer buttons:', out.composer_buttons.map((b) => `[${b.tag}] ${b.aria_label || b.text || b.testid}`).join(' | ') || 'none');
    console.log('  voice-ish controls:');
    for (const v of out.voiceish_controls) {
      console.log(`    [${v.tag}] aria="${v.aria_label || ''}" testid="${v.testid || ''}" title="${v.title || ''}" text="${v.text}" @${v.rect.x},${v.rect.y}`);
    }
  } finally {
    ws.close();
  }
}
