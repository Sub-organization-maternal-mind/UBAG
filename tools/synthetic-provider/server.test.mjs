import assert from 'node:assert/strict';
import test from 'node:test';

import { LIMITS, ScenarioError, chatPage, createSyntheticServer, filler, parseDirective, signedOutPage } from './server.mjs';

async function withServer(opts, fn) {
  const fixture = createSyntheticServer(opts);
  const { url } = await fixture.listen(0);
  try { return await fn(url, fixture); } finally { await fixture.close(); }
}

const chat = (url, prompt, extra = {}) => fetch(`${url}api/chat`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ prompt, ...extra }) });

/** Reads a streamed body, returning chunk arrival times; `error` is set when the stream was cut. */
async function drain(res) {
  const reader = res.body.getReader(); const dec = new TextDecoder();
  const out = { text: '', chunks: 0, firstMs: null, error: null }; const t0 = Date.now();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (value.length) { out.chunks++; out.firstMs ??= Date.now() - t0; out.text += dec.decode(value, { stream: true }); }
    }
  } catch (e) { out.error = e; }
  out.totalMs = Date.now() - t0;
  return out;
}

test('directive: presets, overrides, defaults and fail-closed validation', () => {
  assert.deepEqual(parseDirective('plain prompt').params.chars, 0);
  assert.equal(parseDirective('plain prompt').prompt, 'plain prompt');
  const d = parseDirective('[[synthetic scenario=slow_think think_ms=7]]  hi there');
  assert.equal(d.name, 'slow_think'); assert.equal(d.params.think_ms, 7); assert.equal(d.prompt, 'hi there');
  assert.equal(parseDirective('hi', 'long_output').params.chars, 200_000);
  assert.throws(() => parseDirective('[[synthetic scenario=nope]] x'), ScenarioError);
  assert.throws(() => parseDirective('[[synthetic bogus=1]] x'), /unknown parameter/);
  assert.throws(() => parseDirective('[[synthetic think_ms=-1]] x'), /non-negative integer/);
  assert.throws(() => parseDirective('[[synthetic think_ms=1e3]] x'), /non-negative integer/);
  assert.throws(() => parseDirective(`[[synthetic chars=${LIMITS.chars + 1}]] x`), /exceeds/);
  assert.throws(() => parseDirective('[[synthetic chunk_chars=0]] x'), /at least 1/);
  assert.throws(() => parseDirective('[[synthetic truncate_after=5 drop_after=5]] x'), /mutually exclusive/);
  assert.throws(() => parseDirective('x', 'nope'), ScenarioError);
  assert.equal(filler(1000).length, 1000); assert.equal(filler(1000), filler(1000));
});

test('refuses to bind or answer anything but loopback', async () => {
  assert.throws(() => createSyntheticServer({ host: '0.0.0.0' }), /loopback only/);
  assert.throws(() => createSyntheticServer({ host: '192.168.1.5' }), /loopback only/);
  assert.throws(() => createSyntheticServer({ host: '127.0.0.1.evil.example' }), /loopback only/);
  await withServer({}, async (url) => {
    const { port } = new URL(url);
    const res = await new Promise((ok, bad) => {
      import('node:http').then(({ default: http }) => http.get({ host: '127.0.0.1', port, path: '/healthz', headers: { host: 'evil.example' } }, ok).on('error', bad));
    });
    assert.equal(res.statusCode, 403); res.resume();
    assert.equal((await fetch(`${url}healthz`)).status, 200);
  });
});

test('pages: authenticated markup carries the selector contract; signed-out has no login mechanics', () => {
  const page = chatPage();
  for (const marker of ['app', 'main', 'composer', 'prompt', 'send', 'stop', 'new-chat', 'file-input', 'transcript']) {
    assert.ok(page.includes(`data-synthetic="${marker}"`), `missing marker ${marker}`);
  }
  assert.ok(page.includes("'assistant-turn'"), 'assistant turns are created by the page script');
  assert.match(page, /type="file"[^>]*data-synthetic="file-input"/);
  assert.ok(!page.includes('data-synthetic="sign-in"'));
  const out = signedOutPage();
  assert.ok(out.includes('data-synthetic="sign-in"'));
  for (const forbidden of ['data-synthetic="composer"', 'data-synthetic="prompt"']) assert.ok(!out.includes(forbidden), forbidden);
  for (const html of [page, out]) {
    assert.ok(!/<form/i.test(html), 'no form');
    assert.ok(!/type=["']?password/i.test(html), 'no password input');
    assert.ok(!/captcha|recaptcha|hcaptcha|turnstile/i.test(html), 'no captcha');
    assert.ok(!/https?:\/\//i.test(html), 'no external origin referenced');
  }
});

test('GET / serves the chat page, ?signed_out=1 and the control switch serve the signed-out page, loads are counted', async () => {
  await withServer({}, async (url, fixture) => {
    assert.equal(await (await fetch(url)).text(), chatPage());
    assert.equal(await (await fetch(`${url}?signed_out=1`)).text(), signedOutPage());
    const ctl = await fetch(`${url}__control`, { method: 'POST', body: JSON.stringify({ signed_out: true }) });
    assert.equal((await ctl.json()).signed_out, true);
    assert.equal(await (await fetch(url)).text(), signedOutPage());
    assert.equal(fixture.stats.page_loads, 3); assert.equal(fixture.stats.signed_out_loads, 2);
    assert.equal((await fetch(`${url}__control`, { method: 'POST', body: JSON.stringify({ attach_ms: LIMITS.attach_ms + 1 }) })).status, 400);
    assert.equal((await fetch(`${url}nope`)).status, 404);
  });
});

test('default answer is deterministic and echoes the prompt', async () => {
  await withServer({}, async (url, fixture) => {
    const a = await drain(await chat(url, 'hello world'));
    const b = await drain(await chat(url, 'hello world'));
    assert.equal(a.text, 'Synthetic answer: hello world'); assert.equal(b.text, a.text); assert.equal(a.error, null);
    assert.equal(fixture.stats.chat_requests, 2); assert.equal(fixture.stats.chat_completed, 2);
  });
});

test('slow think: no body byte before think_ms', async () => {
  await withServer({}, async (url) => {
    const res = await chat(url, '[[synthetic scenario=slow_think think_ms=250]] hi');
    assert.equal(res.status, 200); // headers are flushed immediately, the body is what waits
    const r = await drain(res);
    assert.ok(r.firstMs >= 200, `first byte after ${r.firstMs}ms`); assert.equal(r.text, 'Synthetic answer: hi');
  });
});

test('streaming cadence: fixed-size chunks spaced by stream_ms', async () => {
  await withServer({}, async (url) => {
    const r = await drain(await chat(url, '[[synthetic chars=200 chunk_chars=50 stream_ms=60]] go'));
    assert.equal(r.text, filler(200)); assert.ok(r.chunks >= 4, `chunks=${r.chunks}`); assert.ok(r.totalMs >= 150, `total=${r.totalMs}ms`);
  });
});

test('long output: exact length at the cap, rejected above it', async () => {
  await withServer({}, async (url) => {
    const r = await drain(await chat(url, '[[synthetic chars=300000 chunk_chars=8192]] essay'));
    assert.equal(r.text.length, 300_000); assert.equal(r.text, filler(300_000));
    assert.equal((await chat(url, `[[synthetic chars=${LIMITS.chars + 1}]] essay`)).status, 400);
  });
});

test('truncated: stalls after truncate_after chars without a clean end, then is cut', async () => {
  await withServer({}, async (url, fixture) => {
    const r = await drain(await chat(url, '[[synthetic scenario=truncated truncate_after=100 stall_ms=300]] x'));
    assert.equal(r.text, filler(4000).slice(0, 100)); assert.ok(r.error, 'stream must not end cleanly'); assert.ok(r.totalMs >= 250, `stalled ${r.totalMs}ms`);
    assert.equal(fixture.stats.chat_truncated, 1); assert.equal(fixture.stats.chat_completed, 0);
  });
});

test('truncated: a client that leaves during the stall frees the stream', async () => {
  await withServer({}, async (url, fixture) => {
    const ctl = new AbortController();
    const res = await fetch(`${url}api/chat`, { method: 'POST', signal: ctl.signal, body: JSON.stringify({ prompt: '[[synthetic scenario=truncated truncate_after=10 stall_ms=600000]] x' }) });
    const reader = res.body.getReader(); await reader.read();
    assert.equal(fixture.stats.active_streams, 1);
    ctl.abort(); await reader.read().catch(() => {});
    for (let i = 0; i < 50 && fixture.stats.active_streams > 0; i++) await new Promise((ok) => setTimeout(ok, 20));
    assert.equal(fixture.stats.active_streams, 0);
  });
});

test('dropped: connection dies mid-stream after drop_after chars', async () => {
  await withServer({}, async (url, fixture) => {
    const r = await drain(await chat(url, '[[synthetic scenario=dropped drop_after=120 chunk_chars=40]] x'));
    assert.ok(r.error, 'stream must error'); assert.ok(r.text.length <= 120 && filler(4000).startsWith(r.text), `got ${r.text.length} chars`);
    assert.equal(fixture.stats.chat_dropped, 1);
  });
});

test('bad scenario or attachment id is a 400 and counted, never a silent default run', async () => {
  await withServer({}, async (url, fixture) => {
    assert.equal((await chat(url, '[[synthetic scenario=nope]] x')).status, 400);
    assert.equal((await chat(url, 'x', { attachment_ids: ['att_999'] })).status, 400);
    assert.equal((await chat(url, '   ')).status, 400);
    assert.equal((await fetch(`${url}api/chat`, { method: 'POST', body: '{nope' })).status, 400);
    assert.equal(fixture.stats.chat_requests, 0); assert.equal(fixture.stats.chat_rejected, 3);
  });
});

test('slow attachment accept: upload answers after attach_ms and the id is usable in a chat', async () => {
  await withServer({ attachMs: 200 }, async (url, fixture) => {
    const t0 = Date.now();
    const up = await fetch(`${url}api/upload?name=notes.txt`, { method: 'POST', body: 'hello attachment' });
    const meta = await up.json();
    assert.ok(Date.now() - t0 >= 180, 'upload answered too early'); assert.equal(meta.bytes, 16); assert.equal(meta.name, 'notes.txt');
    assert.equal(fixture.stats.uploads, 1); assert.equal(fixture.stats.upload_bytes, 16);
    const r = await drain(await chat(url, 'summarise', { attachment_ids: [meta.id] }));
    assert.equal(r.text, 'Synthetic answer: summarise [attachments: 1]');
    await fetch(`${url}__control`, { method: 'POST', body: JSON.stringify({ attach_ms: 0 }) });
    const t1 = Date.now(); await (await fetch(`${url}api/upload`, { method: 'POST', body: 'x' })).json();
    assert.ok(Date.now() - t1 < 150);
  });
});

test('uploads above the byte cap are rejected with 413', async () => {
  await withServer({ maxUploadBytes: 1024 }, async (url, fixture) => {
    const res = await fetch(`${url}api/upload`, { method: 'POST', body: Buffer.alloc(4096, 1) }).catch((e) => e);
    // The server answers 413 and closes; a client still sending may see the reset instead of the status.
    assert.ok(res instanceof Error || res.status === 413, `status ${res.status}`);
    assert.equal(fixture.stats.uploads, 0);
  });
});

test('stats can be reset and report concurrent streams', async () => {
  await withServer({}, async (url, fixture) => {
    await Promise.all([1, 2, 3].map((i) => chat(url, `[[synthetic think_ms=150]] ${i}`).then(drain)));
    assert.equal(fixture.stats.max_active_streams, 3); assert.equal(fixture.stats.chat_completed, 3);
    await fetch(`${url}__stats/reset`, { method: 'POST' });
    const s = await (await fetch(`${url}__stats`)).json();
    assert.equal(s.chat_requests, 0); assert.equal(s.active_streams, 0);
  });
});
