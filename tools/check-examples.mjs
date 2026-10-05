#!/usr/bin/env node
// Offline check for examples/: syntax-checks every example (JS, the inline
// module script of voice-browser.html, Python, Go via `go vet`, shell via
// `bash -n`) and smoke-tests voice-backend.mjs against a fake gateway to prove
// the API key stays server-side. Toolchains that are not installed are
// reported as skipped, so Node-only environments still pass.
import { createServer } from 'node:http';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('.', import.meta.url)), '..');
const examples = join(root, 'examples');
let failures = 0;
const fail = (message) => { failures += 1; console.error(`FAIL ${message}`); };
const skip = (message) => console.warn(`SKIP ${message}`);
const files = (dir, ext) => readdirSync(dir).filter((name) => name.endsWith(ext)).map((name) => join(dir, name));
const run = (cmd, args, options = {}) => spawnSync(cmd, args, { encoding: 'utf8', ...options });
const have = (cmd, args = ['--version']) => { const r = run(cmd, args); return !r.error && r.status === 0; };

// JavaScript (+ the inline module script in the browser page)
for (const file of files(join(examples, 'javascript'), '.mjs')) {
  const result = run(process.execPath, ['--check', file]);
  if (result.status !== 0) fail(`${file}\n${result.stderr}`);
}
{
  const html = readFileSync(join(examples, 'javascript', 'voice-browser.html'), 'utf8');
  const script = /<script type="module">([\s\S]*?)<\/script>/.exec(html);
  if (!script) {
    fail('voice-browser.html has no inline module script');
  } else {
    const dir = mkdtempSync(join(tmpdir(), 'ubag-example-'));
    const file = join(dir, 'voice-browser.mjs');
    writeFileSync(file, script[1]);
    const result = run(process.execPath, ['--check', file]);
    if (result.status !== 0) fail(`voice-browser.html inline script\n${result.stderr}`);
    rmSync(dir, { recursive: true, force: true });
  }
  if (/UBAG_TOKEN|appSecret|Authorization/.test(html)) fail('voice-browser.html must not reference the API key');
}

// Python (compile only; no .pyc written)
const python = ['python', 'python3'].find((cmd) => have(cmd));
if (!python) {
  skip('python not found; Python examples not checked');
} else {
  for (const file of files(join(examples, 'python'), '.py')) {
    const result = run(python, ['-c', 'import sys; compile(open(sys.argv[1], encoding="utf-8").read(), sys.argv[1], "exec")', file]);
    if (result.status !== 0) fail(`${file}\n${result.stderr}`);
  }
}

// Go
const go = findGo();
if (!go) {
  skip('go not found; Go examples not checked');
} else {
  const result = run(go, ['vet', './...'], { cwd: join(examples, 'go'), env: { ...process.env, GOTOOLCHAIN: 'local' } });
  if (result.status !== 0) fail(`go vet examples/go\n${result.stdout}${result.stderr}`);
}

// Shell
if (!have('bash')) {
  skip('bash not found; HTTP examples not checked');
} else {
  for (const file of files(join(examples, 'http'), '.sh')) {
    const result = run('bash', ['-n', file]);
    if (result.status !== 0) fail(`${file}\n${result.stderr}`);
  }
}

// Backend smoke test: the browser-facing API never exposes the API key and
// only lets a browser touch sessions this backend created.
if (!existsSync(join(root, 'packages/sdk-typescript/dist/index.js'))) {
  skip('packages/sdk-typescript/dist missing (pnpm --filter @ubag/sdk build); voice-backend smoke test not run');
} else {
  await smokeBackend().catch((error) => fail(`voice-backend smoke test: ${error.stack ?? error}`));
}

if (failures > 0) {
  console.error(`${failures} example check(s) failed.`);
  process.exit(1);
}
console.log('Example checks passed.');

async function smokeBackend() {
  const secret = 'example-secret-do-not-leak';
  const seen = [];
  const gateway = createServer((req, res) => {
    let raw = '';
    req.on('data', (chunk) => { raw += chunk; });
    req.on('end', () => {
      seen.push({ method: req.method, path: req.url, auth: req.headers.authorization, body: raw ? JSON.parse(raw) : undefined });
      res.setHeader('content-type', 'application/json');
      if (req.url === '/v1/voice/sessions') {
        res.statusCode = 201;
        res.end(JSON.stringify({ kind: 'voice_session', session_id: 'voice_1', status: 'connecting', session: {} }));
      } else if (req.url.endsWith('/connect')) {
        res.end(JSON.stringify({ kind: 'voice_session_connection', session_id: 'voice_1', status: 'connecting', sdp_answer: 'v=0', media_credential: 'scoped-cred', ice_servers: [{ urls: ['stun:s'] }] }));
      } else {
        res.end(JSON.stringify({ kind: 'voice_session', session: { status: 'terminated' } }));
      }
    });
  });
  const listen = (server) => new Promise((ok) => server.listen(0, '127.0.0.1', () => ok(server.address().port)));
  const gatewayPort = await listen(gateway);
  const probe = createServer();
  const port = await listen(probe);
  await new Promise((ok) => probe.close(ok));

  const child = spawn(process.execPath, [join(examples, 'javascript', 'voice-backend.mjs')], {
    env: { ...process.env, UBAG_TOKEN: secret, UBAG_BASE_URL: `http://127.0.0.1:${gatewayPort}`, PORT: String(port) },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  try {
    await new Promise((ok, reject) => {
      child.once('exit', (code) => reject(new Error(`backend exited early (${code})`)));
      child.stdout.on('data', ok);
    });
    const post = (path, body) => fetch(`http://127.0.0.1:${port}${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body ?? {}) });
    const bodies = [];
    const check = async (response, status, label) => {
      const text = await response.text();
      bodies.push(text);
      if (response.status !== status) throw new Error(`${label}: expected ${status}, got ${response.status} ${text}`);
      return JSON.parse(text);
    };

    const created = await check(await post('/api/voice/sessions', { target: 'chatgpt_web' }), 200, 'create');
    const connect = await check(await post(`/api/voice/sessions/${created.session_id}/connect`, { sdp_offer: 'offer' }), 200, 'connect');
    if (connect.media_credential !== 'scoped-cred' || !connect.ice_servers?.length) throw new Error('connect response lost media_credential/ice_servers');
    await check(await post('/api/voice/sessions/voice_other/connect', { sdp_offer: 'offer' }), 404, 'foreign session');
    await check(await post(`/api/voice/sessions/${created.session_id}/terminate`), 200, 'terminate');
    await check(await post(`/api/voice/sessions/${created.session_id}/connect`, { sdp_offer: 'offer' }), 404, 'connect after terminate');

    if (bodies.some((body) => body.includes(secret))) throw new Error('API key leaked into a browser-facing response');
    if (!seen.every((call) => call.auth === `Bearer ${secret}`)) throw new Error('backend did not authenticate to the gateway');
    if (seen.some((call) => call.path.includes('voice_other'))) throw new Error('backend forwarded a session it does not own');
  } finally {
    child.kill();
    gateway.close();
  }
}

function findGo() {
  if (have('go', ['version'])) return 'go';
  const base = process.env.LOCALAPPDATA && join(process.env.LOCALAPPDATA, 'CodexToolchains');
  if (!base || !existsSync(base)) return null;
  return readdirSync(base)
    .filter((name) => name.startsWith('go'))
    .map((name) => join(base, name, 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go'))
    .filter((path) => existsSync(path))
    .sort()
    .reverse()[0] ?? null;
}
