#!/usr/bin/env node
// Synthetic chat provider (P7.1): a loopback-only fake "web chat" that the worker's live engine can drive
// through a real browser, so load/acceptance scenarios exercise the warm-daemon path (the mock target
// bypasses it). Zero dependencies.
//
//   node tools/synthetic-provider/server.mjs [--port 4799] [--scenario slow_think] [--attach-ms 0]
//
// Safe-mode: binds loopback only, answers only loopback Host headers, and has NO login, credential,
// password or CAPTCHA behaviour (the signed-out page is a bare "Sign in" anchor that goes nowhere).
//
// Scenarios are picked per prompt with a leading directive, e.g.
//   [[synthetic scenario=slow_think think_ms=2500]] explain X
//   [[synthetic chars=200000 chunk_chars=4096 stream_ms=5]] write a long essay
// Presets: slow_think, stream_cadence, long_output, truncated (stalls, Stop control stays up), dropped
// (connection killed mid-stream). Explicit keys override a preset. Unknown names or out-of-range values are
// rejected with HTTP 400 (fail closed) so a typo can never silently run the wrong scenario.
// Signed-out page: GET /?signed_out=1 (or POST /__control {"signed_out":true}).
// Slow attachment accept: POST /__control {"attach_ms":1500} or --attach-ms.
// Counters for warm-vs-cold analysis: GET /__stats (POST /__stats/reset).
import http from 'node:http';
import { isIP } from 'node:net';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

export const DEFAULT_PORT = 4799;
export const LIMITS = Object.freeze({
  chars: 2 * 1024 * 1024, think_ms: 120_000, stream_ms: 10_000, chunk_chars: 65_536,
  stall_ms: 900_000, attach_ms: 120_000, truncate_after: 2 * 1024 * 1024, drop_after: 2 * 1024 * 1024,
  chatBodyBytes: 1024 * 1024, uploadBytes: 64 * 1024 * 1024, attachments: 1024,
});
const BASE = Object.freeze({ think_ms: 0, stream_ms: 0, chunk_chars: 64, chars: 0, truncate_after: null, drop_after: null, stall_ms: 600_000 });
export const SCENARIOS = Object.freeze({
  default: {},
  slow_think: { think_ms: 3000 },
  stream_cadence: { chars: 600, chunk_chars: 24, stream_ms: 120 },
  long_output: { chars: 200_000, chunk_chars: 4096 },
  truncated: { chars: 4000, truncate_after: 600 },
  dropped: { chars: 4000, drop_after: 600 },
});
const INT_KEYS = ['think_ms', 'stream_ms', 'chunk_chars', 'chars', 'truncate_after', 'drop_after', 'stall_ms'];

export class ScenarioError extends Error {}

/** Splits a leading `[[synthetic k=v ...]]` directive off a prompt. Throws ScenarioError on anything unknown. */
export function parseDirective(prompt, defaultScenario = 'default') {
  const m = /^\s*\[\[synthetic\b([^\]]*)\]\]\s*/.exec(prompt);
  const tokens = m ? m[1].trim().split(/\s+/).filter(Boolean) : [];
  const kv = {};
  for (const t of tokens) {
    const i = t.indexOf('=');
    if (i < 1) throw new ScenarioError(`bad directive token "${t.slice(0, 40)}"`);
    kv[t.slice(0, i)] = t.slice(i + 1);
  }
  const name = kv.scenario ?? defaultScenario;
  delete kv.scenario;
  if (!Object.hasOwn(SCENARIOS, name)) throw new ScenarioError(`unknown scenario "${String(name).slice(0, 40)}"`);
  const params = { ...BASE, ...SCENARIOS[name] };
  for (const [k, v] of Object.entries(kv)) {
    if (!INT_KEYS.includes(k)) throw new ScenarioError(`unknown parameter "${k.slice(0, 40)}"`);
    if (!/^\d{1,9}$/.test(v)) throw new ScenarioError(`parameter ${k} must be a non-negative integer`);
    params[k] = Number(v);
  }
  for (const k of INT_KEYS) {
    if (params[k] !== null && params[k] > LIMITS[k]) throw new ScenarioError(`parameter ${k} exceeds ${LIMITS[k]}`);
  }
  if (params.chunk_chars < 1) throw new ScenarioError('chunk_chars must be at least 1');
  if (params.truncate_after !== null && params.drop_after !== null) throw new ScenarioError('truncate_after and drop_after are mutually exclusive');
  return { name, params, prompt: m ? prompt.slice(m[0].length) : prompt };
}

const UNIT = 'alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega. ';
/** Deterministic filler of exactly `chars` characters (same input, same bytes). */
export const filler = (chars) => UNIT.repeat(Math.ceil(chars / UNIT.length)).slice(0, chars);

const isLoopbackHost = (h) => h === 'localhost' || h === '::1' || (isIP(h) === 4 && h.startsWith('127.'));
const hostnameOf = (hostHeader) => { try { return new URL(`http://${hostHeader}`).hostname.replace(/^\[|\]$/g, '').toLowerCase(); } catch { return ''; } };

const PAGE_CSS = 'body{font:15px system-ui,sans-serif;margin:0;display:flex;flex-direction:column;height:100vh}header{padding:8px 16px;border-bottom:1px solid #ccc;display:flex;gap:12px;align-items:center}'
  + 'main{flex:1;overflow:auto;padding:16px}[data-synthetic=user-turn],[data-synthetic=assistant-turn]{display:block;min-height:1.4em;margin:8px 0;padding:8px 12px;border-radius:8px;white-space:pre-wrap}'
  + '[data-synthetic=user-turn]{background:#eef}[data-synthetic=assistant-turn]{background:#efe}[data-state=error]{background:#fee}[hidden]{display:none!important}'
  + 'footer{border-top:1px solid #ccc;padding:8px 16px;display:flex;gap:8px;align-items:flex-start;flex-wrap:wrap}textarea{flex:1;min-width:200px;min-height:3em}';

const CHAT_JS = `(() => {
  const $ = (n) => document.querySelector('[data-synthetic="' + n + '"]');
  const transcript = $('transcript'), prompt = $('prompt'), send = $('send'), stop = $('stop'), thinking = $('thinking');
  const errorBox = $('error'), file = $('file-input'), chips = $('attachments');
  let ctl = null, pending = 0, uploaded = [];
  const sync = () => { send.disabled = !!ctl || pending > 0 || !prompt.value.trim(); };
  const fail = (m) => { errorBox.textContent = m; errorBox.hidden = false; };
  const turn = (role, text) => { const d = document.createElement('div'); d.dataset.synthetic = role === 'user' ? 'user-turn' : 'assistant-turn'; if (text) d.textContent = text; transcript.appendChild(d); return d; };
  async function submit() {
    const text = prompt.value.trim();
    if (!text || ctl || pending > 0) return;
    errorBox.hidden = true;
    turn('user', text);
    const out = turn('assistant', ''), node = document.createTextNode('');
    out.appendChild(node); out.dataset.state = 'streaming';
    const ids = uploaded.splice(0); chips.textContent = ''; prompt.value = '';
    ctl = new AbortController(); stop.hidden = false; thinking.hidden = false; sync();
    try {
      const res = await fetch('/api/chat', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ prompt: text, attachment_ids: ids }), signal: ctl.signal });
      if (!res.ok) { out.remove(); fail('request rejected (' + res.status + ')'); return; }
      const reader = res.body.getReader(), dec = new TextDecoder();
      for (;;) { const r = await reader.read(); if (r.done) break; if (r.value.length) { thinking.hidden = true; node.appendData(dec.decode(r.value, { stream: true })); } }
      node.appendData(dec.decode());
      out.dataset.state = 'done';
    } catch (e) {
      if (e && e.name === 'AbortError') out.dataset.state = 'stopped';
      else { out.dataset.state = 'error'; fail('connection lost'); }
    } finally { ctl = null; stop.hidden = true; thinking.hidden = true; sync(); }
  }
  send.addEventListener('click', submit);
  stop.addEventListener('click', () => { if (ctl) ctl.abort(); });
  prompt.addEventListener('input', sync);
  prompt.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submit(); } });
  $('new-chat').addEventListener('click', () => {
    if (ctl) ctl.abort();
    transcript.replaceChildren(); chips.textContent = ''; uploaded = []; prompt.value = ''; errorBox.hidden = true; sync();
  });
  file.addEventListener('change', async () => {
    for (const f of Array.from(file.files)) {
      const chip = document.createElement('div'); chip.dataset.synthetic = 'attachment'; chip.dataset.state = 'uploading'; chip.textContent = f.name; chips.appendChild(chip);
      pending++; sync();
      try {
        const r = await fetch('/api/upload?name=' + encodeURIComponent(f.name), { method: 'POST', body: f });
        if (!r.ok) throw new Error('upload');
        uploaded.push((await r.json()).id); chip.dataset.state = 'ready';
      } catch (e) { chip.dataset.state = 'error'; fail('upload failed'); } finally { pending--; sync(); }
    }
    file.value = '';
  });
  sync();
})();`;

export const chatPage = () => `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Synthetic Chat (UBAG test fixture)</title><style>${PAGE_CSS}</style></head>
<body data-synthetic="app" data-auth="authenticated">
<header><strong>Synthetic Chat</strong><button type="button" data-synthetic="new-chat">New chat</button><span data-synthetic="thinking" hidden>Thinking...</span></header>
<main data-synthetic="main"><div data-synthetic="transcript"></div><div data-synthetic="error" role="alert" hidden></div></main>
<footer data-synthetic="composer"><div data-synthetic="attachments"></div>
<textarea data-synthetic="prompt" placeholder="Message Synthetic Chat"></textarea>
<input type="file" data-synthetic="file-input" multiple hidden>
<button type="button" data-synthetic="send" disabled>Send</button><button type="button" data-synthetic="stop" hidden>Stop</button></footer>
<script>${CHAT_JS}</script></body></html>`;

export const signedOutPage = () => `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Synthetic Chat - signed out</title><style>${PAGE_CSS}</style></head>
<body data-synthetic="app" data-auth="signed_out"><header><strong>Synthetic Chat</strong></header>
<main data-synthetic="main"><p>You are signed out.</p><a data-synthetic="sign-in" href="#signin">Sign in</a></main></body></html>`;

/** Create (not yet listening) the fixture server. `options.host` must be loopback; refuses anything else. */
export function createSyntheticServer(options = {}) {
  const host = options.host ?? '127.0.0.1';
  if (!isLoopbackHost(host)) throw new Error(`synthetic provider binds loopback only (got "${host}")`);
  const state = { signedOut: !!options.signedOut, attachMs: Math.min(Math.max(0, options.attachMs ?? 0), LIMITS.attach_ms) };
  const defaultScenario = options.scenario ?? 'default';
  const maxUpload = options.maxUploadBytes ?? LIMITS.uploadBytes;
  const attachments = new Map();
  let seq = 0;
  const zero = () => ({ page_loads: 0, signed_out_loads: 0, chat_requests: 0, chat_completed: 0, chat_truncated: 0, chat_dropped: 0, chat_rejected: 0, uploads: 0, upload_bytes: 0, active_streams: 0, max_active_streams: 0 });
  let stats = zero();

  const json = (res, code, body) => {
    const b = JSON.stringify(body);
    res.writeHead(code, { 'content-type': 'application/json', 'cache-control': 'no-store', 'content-length': Buffer.byteLength(b), ...(code === 413 ? { connection: 'close' } : {}) });
    res.end(b);
  };
  // Buffers at most `max` bytes; past that it stops buffering (the 413 is sent while the rest is drained).
  const readBody = (req, max) => new Promise((ok, bad) => {
    const parts = []; let n = 0, over = false;
    req.on('data', (c) => { if (over) return; n += c.length; if (n > max) { over = true; parts.length = 0; bad(Object.assign(new Error('body too large'), { code: 413 })); } else parts.push(c); });
    req.on('end', () => { if (!over) ok(Buffer.concat(parts)); });
    req.on('error', bad); req.on('aborted', () => bad(new Error('aborted')));
  });

  async function chat(req, res) {
    let body;
    try { body = JSON.parse((await readBody(req, LIMITS.chatBodyBytes)).toString('utf8')); } catch (e) { return json(res, e.code === 413 ? 413 : 400, { error: 'invalid chat body' }); }
    const reject = (msg) => { stats.chat_rejected++; return json(res, 400, { error: msg }); };
    if (!body || typeof body.prompt !== 'string' || !body.prompt.trim()) return reject('prompt is required');
    const ids = Array.isArray(body.attachment_ids) ? body.attachment_ids : [];
    if (ids.some((id) => typeof id !== 'string' || !attachments.has(id))) return reject('unknown attachment id');
    let d;
    try { d = parseDirective(body.prompt, defaultScenario); } catch (e) { if (e instanceof ScenarioError) return reject(e.message); throw e; }
    stats.chat_requests++;
    const text = d.params.chars > 0 ? filler(d.params.chars)
      : `Synthetic answer: ${d.prompt.slice(0, 200)}${ids.length ? ` [attachments: ${ids.length}]` : ''}`;
    await stream(res, text, d.params);
  }

  async function stream(res, text, p) {
    const gate = { closed: false, wake: new Set() };
    res.on('close', () => { gate.closed = true; for (const w of [...gate.wake]) w(); });
    const sleep = (ms) => new Promise((ok) => {
      if (gate.closed) return ok();
      const done = () => { clearTimeout(t); gate.wake.delete(done); ok(); };
      const t = setTimeout(done, ms); gate.wake.add(done);
    });
    const drain = () => new Promise((ok) => { const done = () => { res.off('drain', done); gate.wake.delete(done); ok(); }; res.once('drain', done); gate.wake.add(done); });
    stats.active_streams++; stats.max_active_streams = Math.max(stats.max_active_streams, stats.active_streams);
    try {
      res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' });
      res.flushHeaders();
      await sleep(p.think_ms);
      const cut = p.truncate_after ?? p.drop_after;
      const end = cut === null ? text.length : Math.min(cut, text.length);
      for (let sent = 0; sent < end && !gate.closed;) {
        const next = Math.min(sent + p.chunk_chars, end);
        if (!res.write(text.slice(sent, next))) await drain();
        sent = next;
        if (sent < end) await (p.stream_ms > 0 ? sleep(p.stream_ms) : new Promise((ok) => setImmediate(ok)));
      }
      if (gate.closed) return;
      if (end < text.length && p.truncate_after !== null) { stats.chat_truncated++; await sleep(p.stall_ms); res.socket?.destroy(); return; }
      if (end < text.length) { stats.chat_dropped++; res.socket?.destroySoon(); return; }
      stats.chat_completed++; res.end();
    } finally { stats.active_streams--; }
  }

  async function upload(req, res, url) {
    let data;
    try { data = await readBody(req, maxUpload); } catch (e) { return json(res, e.code === 413 ? 413 : 400, { error: 'upload rejected' }); }
    const delay = state.attachMs;
    await new Promise((ok) => setTimeout(ok, delay).unref());
    const id = `att_${++seq}`;
    attachments.set(id, { bytes: data.length });
    if (attachments.size > LIMITS.attachments) attachments.delete(attachments.keys().next().value);
    stats.uploads++; stats.upload_bytes += data.length;
    json(res, 200, { id, name: (url.searchParams.get('name') ?? '').slice(0, 200), bytes: data.length });
  }

  const server = http.createServer(async (req, res) => {
    try {
      if (!isLoopbackHost(hostnameOf(req.headers.host ?? ''))) return json(res, 403, { error: 'loopback Host required' });
      const url = new URL(req.url ?? '/', 'http://localhost');
      const route = `${req.method} ${url.pathname}`;
      res.setHeader('x-synthetic-provider', '1');
      if (route === 'GET /') {
        const out = state.signedOut || url.searchParams.get('signed_out') === '1';
        stats.page_loads++; if (out) stats.signed_out_loads++;
        res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
        return res.end(out ? signedOutPage() : chatPage());
      }
      if (route === 'GET /healthz') return json(res, 200, { ok: true });
      if (route === 'GET /__stats') return json(res, 200, { ...stats, signed_out: state.signedOut, attach_ms: state.attachMs });
      if (route === 'POST /__stats/reset') { stats = { ...zero(), active_streams: stats.active_streams }; return json(res, 200, { ok: true }); }
      if (route === 'POST /__control') {
        const c = JSON.parse((await readBody(req, 4096)).toString('utf8') || '{}');
        if (c.signed_out !== undefined) state.signedOut = c.signed_out === true;
        if (c.attach_ms !== undefined) {
          if (!Number.isInteger(c.attach_ms) || c.attach_ms < 0 || c.attach_ms > LIMITS.attach_ms) return json(res, 400, { error: 'attach_ms out of range' });
          state.attachMs = c.attach_ms;
        }
        return json(res, 200, { signed_out: state.signedOut, attach_ms: state.attachMs });
      }
      if (route === 'POST /api/chat') return await chat(req, res);
      if (route === 'POST /api/upload') return await upload(req, res, url);
      return json(res, 404, { error: 'not found' });
    } catch {
      if (!res.headersSent) json(res, 400, { error: 'bad request' }); else res.destroy();
    }
  });
  server.keepAliveTimeout = 5000;

  return {
    server, host, state,
    get stats() { return { ...stats }; },
    listen: (port = DEFAULT_PORT) => new Promise((ok, bad) => {
      server.once('error', bad);
      server.listen(port, host, () => { server.off('error', bad); const a = server.address(); ok({ port: a.port, url: `http://${host.includes(':') ? `[${host}]` : host}:${a.port}/` }); });
    }),
    close: () => new Promise((ok) => { server.closeAllConnections?.(); server.close(() => ok()); }),
  };
}

function cli(argv) {
  const opt = {}; let port = DEFAULT_PORT;
  for (let i = 0; i < argv.length; i++) {
    const [flag, inline] = argv[i].split('=', 2); const val = () => inline ?? argv[++i];
    if (flag === '--port') port = Number(val());
    else if (flag === '--host') opt.host = val();
    else if (flag === '--scenario') opt.scenario = val();
    else if (flag === '--attach-ms') opt.attachMs = Number(val());
    else if (flag === '--signed-out') opt.signedOut = true;
    else { console.error(`unknown argument ${flag}`); return 2; }
  }
  if (!Number.isInteger(port) || port < 0 || port > 65535) { console.error('invalid --port'); return 2; }
  if (opt.attachMs !== undefined && !Number.isInteger(opt.attachMs)) { console.error('invalid --attach-ms'); return 2; }
  try { parseDirective('', opt.scenario ?? 'default'); } catch (e) { console.error(e.message); return 2; }
  let fixture;
  try { fixture = createSyntheticServer(opt); } catch (e) { console.error(e.message); return 2; }
  fixture.listen(port).then((a) => {
    console.log(JSON.stringify({ listening: a.url }));
    const stop = () => fixture.close().then(() => process.exit(0));
    process.on('SIGINT', stop); process.on('SIGTERM', stop);
  }, (e) => { console.error(`listen failed: ${e.message}`); process.exit(1); });
  return null;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const code = cli(process.argv.slice(2));
  if (code !== null) process.exit(code);
}
