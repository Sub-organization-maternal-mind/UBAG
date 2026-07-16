#!/usr/bin/env node
// UBAG local live-browser bridge (no Docker, no VNC).
//
// Launches a real Chrome with a PERSISTENT profile (so provider logins survive
// restarts), attaches over the Chrome DevTools Protocol, streams JPEG frames to
// the dashboard over a WebSocket, and forwards mouse/keyboard input back into
// Chrome via CDP Input.*. This lets an operator open chatgpt.com / claude.ai /
// etc. and log in interactively from inside the dashboard's Browser Sessions
// panel — the ToS-safe "human logs in once, in their own session" model.
//
// Zero external dependencies: Node's built-in global WebSocket (client, for
// CDP), a hand-rolled RFC6455 WebSocket server (for the dashboard), fetch,
// http, crypto, child_process. Requires Node 22+ (global WebSocket).
//
// Local development only; not part of the build/test pipeline.

import http from 'node:http';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

const WS_PORT = Number(process.env.UBAG_LIVE_BROWSER_PORT ?? 58090);
const CDP_PORT = Number(process.env.UBAG_LIVE_BROWSER_CDP_PORT ?? 58091);
const PROFILE_DIR = process.env.UBAG_LIVE_BROWSER_PROFILE
  ? resolve(process.env.UBAG_LIVE_BROWSER_PROFILE)
  : resolve(here, 'chrome-profile');
const START_URL = process.env.UBAG_LIVE_BROWSER_START_URL ?? 'https://chatgpt.com';
const FRAME_QUALITY = Number(process.env.UBAG_LIVE_BROWSER_QUALITY ?? 60);
const FRAME_MAX_WIDTH = Number(process.env.UBAG_LIVE_BROWSER_MAX_WIDTH ?? 1280);

function log(...args) {
  console.log(new Date().toISOString(), '[live-browser]', ...args);
}

// Set by main(); called whenever Chrome dies or a capture fails so the bridge
// relaunches Chrome and re-attaches instead of streaming a dead browser.
let triggerRecover = () => {};

// ---------------------------------------------------------------------------
// Chrome discovery + launch
// ---------------------------------------------------------------------------

function findChrome() {
  if (process.env.UBAG_CHROME_PATH && existsSync(process.env.UBAG_CHROME_PATH)) {
    return process.env.UBAG_CHROME_PATH;
  }
  const candidates = [
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
    process.env.LOCALAPPDATA
      ? process.env.LOCALAPPDATA + '\\Google\\Chrome\\Application\\chrome.exe'
      : null,
    'C:\\Program Files\\Google\\Chrome Beta\\Application\\chrome.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
  ].filter(Boolean);
  for (const c of candidates) {
    if (existsSync(c)) return c;
  }
  return null;
}

async function cdpAlive() {
  try {
    const r = await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`, {
      signal: AbortSignal.timeout(1500),
    });
    return r.ok;
  } catch {
    return false;
  }
}

async function ensureChrome() {
  if (await cdpAlive()) {
    log(`reusing Chrome already on CDP port ${CDP_PORT}`);
    return;
  }
  const chrome = findChrome();
  if (!chrome) {
    log('ERROR: could not find Chrome/Edge. Set UBAG_CHROME_PATH to the browser executable.');
    process.exit(1);
  }
  mkdirSync(PROFILE_DIR, { recursive: true });
  const args = [
    `--remote-debugging-port=${CDP_PORT}`,
    // Bind DevTools to loopback only.
    '--remote-debugging-address=127.0.0.1',
    `--user-data-dir=${PROFILE_DIR}`,
    '--no-first-run',
    '--no-default-browser-check',
    // Keep the compositor painting even when the window is unfocused,
    // occluded, or minimized. Without these, headed Chrome on Windows throttles
    // an occluded window and Page.startScreencast stops emitting frames — which
    // is exactly what happens here, since the operator drives the browser via
    // the dashboard canvas and the real Chrome window sits in the background.
    '--disable-features=CalculateNativeWinOcclusion',
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
    '--disable-background-timer-throttling',
    // Deliberately NOT passing --enable-automation, so provider sites see a
    // normal Chrome (no "controlled by automated software" banner) and the
    // human's manual login behaves normally. Safe-mode: the human logs in.
    '--new-window',
    START_URL,
  ];
  log(`launching ${chrome} (profile: ${PROFILE_DIR})`);
  const child = spawn(chrome, args, { detached: false, stdio: 'ignore' });
  child.on('exit', (code) => {
    log(`Chrome exited (code ${code})`);
    // The operator likely closed the window (it looks like a stray Chrome).
    // Relaunch + re-attach so the dashboard view recovers on its own.
    triggerRecover();
  });

  // Wait for CDP to come up.
  for (let i = 0; i < 40; i++) {
    if (await cdpAlive()) {
      log('Chrome CDP is up');
      return;
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  log('ERROR: Chrome CDP did not become reachable');
  process.exit(1);
}

// ---------------------------------------------------------------------------
// CDP page session: one attached page target + screencast + input dispatch
// ---------------------------------------------------------------------------

class PageSession {
  constructor(onFrame, onMeta) {
    this.onFrame = onFrame; // (base64Jpeg) => void
    this.onMeta = onMeta; // ({deviceWidth, deviceHeight, url, targetId}) => void
    this.ws = null;
    this.nextId = 1;
    this.pending = new Map();
    this.deviceWidth = FRAME_MAX_WIDTH;
    this.deviceHeight = 720;
    this.targetId = null;
  }

  async listPageTargets() {
    const r = await fetch(`http://127.0.0.1:${CDP_PORT}/json/list`);
    const all = await r.json();
    return all.filter((t) => t.type === 'page');
  }

  send(method, params = {}) {
    return new Promise((resolvePromise, rejectPromise) => {
      const id = this.nextId++;
      this.pending.set(id, { resolvePromise, rejectPromise });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async attach(targetId) {
    const targets = await this.listPageTargets();
    let target = targetId ? targets.find((t) => t.id === targetId) : null;
    if (!target) target = targets[0];
    if (!target) throw new Error('no page target available');
    this.targetId = target.id;

    if (this.ws) {
      try { this.ws.close(); } catch { /* ignore */ }
      this.ws = null;
    }

    await new Promise((resolvePromise, rejectPromise) => {
      const ws = new WebSocket(target.webSocketDebuggerUrl);
      this.ws = ws;
      ws.onopen = () => resolvePromise();
      ws.onerror = (e) => rejectPromise(e?.error ?? new Error('CDP ws error'));
      ws.onclose = () => { if (this.ws === ws) this.ws = null; };
      ws.onmessage = (ev) => this.onCdpMessage(ev.data);
    });

    await this.send('Page.enable');
    await this.send('Runtime.enable').catch(() => {});
    await this.startScreencast();
    this.onMeta({ deviceWidth: this.deviceWidth, deviceHeight: this.deviceHeight, url: target.url, targetId: this.targetId });
    log(`attached to page ${this.targetId} (${target.url})`);
  }

  async startScreencast() {
    await this.send('Page.startScreencast', {
      format: 'jpeg',
      quality: FRAME_QUALITY,
      maxWidth: FRAME_MAX_WIDTH,
      everyNthFrame: 1,
    });
  }

  // One-shot current-state capture, independent of the screencast's
  // change-driven frames. Used to give a newly connected client an immediate
  // frame (a static page emits no screencast frame until it changes), and as a
  // low-rate keepalive so the canvas always reflects the live page.
  async captureOnce() {
    if (!this.ws) return null;
    try {
      const res = await this.send('Page.captureScreenshot', {
        format: 'jpeg',
        quality: FRAME_QUALITY,
      });
      return res?.data ?? null;
    } catch {
      return null;
    }
  }

  onCdpMessage(raw) {
    let msg;
    try { msg = JSON.parse(raw); } catch { return; }
    if (msg.id && this.pending.has(msg.id)) {
      const { resolvePromise, rejectPromise } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) rejectPromise(new Error(msg.error.message));
      else resolvePromise(msg.result);
      return;
    }
    if (msg.method === 'Page.screencastFrame') {
      const { data, sessionId, metadata } = msg.params;
      if (metadata && metadata.deviceWidth) {
        this.deviceWidth = metadata.deviceWidth;
        this.deviceHeight = metadata.deviceHeight;
      }
      // Ack so Chrome keeps sending frames.
      this.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
      this.onFrame(data);
    } else if (msg.method === 'Page.frameNavigated' && msg.params?.frame && !msg.params.frame.parentId) {
      this.onMeta({ deviceWidth: this.deviceWidth, deviceHeight: this.deviceHeight, url: msg.params.frame.url, targetId: this.targetId });
    }
  }

  // --- input ---
  mouse(kind, fx, fy, button, deltaY) {
    if (!this.ws) return;
    const x = Math.round(fx * this.deviceWidth);
    const y = Math.round(fy * this.deviceHeight);
    if (kind === 'wheel') {
      this.send('Input.dispatchMouseEvent', {
        type: 'mouseWheel', x, y, deltaX: 0, deltaY: deltaY ?? 0,
      }).catch(() => {});
      return;
    }
    const typeMap = { move: 'mouseMoved', down: 'mousePressed', up: 'mouseReleased' };
    const b = button === 2 ? 'right' : button === 1 ? 'middle' : 'left';
    this.send('Input.dispatchMouseEvent', {
      type: typeMap[kind], x, y,
      button: kind === 'move' ? 'none' : b,
      buttons: kind === 'down' ? 1 : 0,
      clickCount: kind === 'down' || kind === 'up' ? 1 : 0,
    }).catch(() => {});
  }

  text(str) {
    if (!this.ws) return;
    this.send('Input.insertText', { text: str }).catch(() => {});
  }

  key(kind, keyInfo) {
    if (!this.ws) return;
    this.send('Input.dispatchKeyEvent', {
      type: kind === 'down' ? 'keyDown' : 'keyUp',
      key: keyInfo.key,
      code: keyInfo.code,
      windowsVirtualKeyCode: keyInfo.keyCode,
      nativeVirtualKeyCode: keyInfo.keyCode,
    }).catch(() => {});
  }

  navigate(url) {
    if (!this.ws) return;
    this.send('Page.navigate', { url }).catch(() => {});
  }

  async newTab(url) {
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?${encodeURIComponent(url || START_URL)}`, {
      method: 'PUT',
    }).catch(async () => {
      // Older Chrome uses GET for /json/new.
      await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?${encodeURIComponent(url || START_URL)}`).catch(() => {});
    });
  }
}

// ---------------------------------------------------------------------------
// Minimal RFC6455 WebSocket server (dashboard <-> bridge)
// ---------------------------------------------------------------------------

function wsAccept(key) {
  return crypto
    .createHash('sha1')
    .update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11')
    .digest('base64');
}

function encodeFrame(data, opcode) {
  const len = data.length;
  let header;
  if (len < 126) {
    header = Buffer.from([0x80 | opcode, len]);
  } else if (len < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 126;
    header.writeUInt16BE(len, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 127;
    header.writeBigUInt64BE(BigInt(len), 2);
  }
  return Buffer.concat([header, data]);
}

// Stateful per-connection frame decoder for client->server (masked) frames.
function makeDecoder(onText, onClose) {
  let buf = Buffer.alloc(0);
  return (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      if (buf.length < 2) return;
      const opcode = buf[0] & 0x0f;
      const masked = (buf[1] & 0x80) !== 0;
      let len = buf[1] & 0x7f;
      let offset = 2;
      if (len === 126) {
        if (buf.length < 4) return;
        len = buf.readUInt16BE(2);
        offset = 4;
      } else if (len === 127) {
        if (buf.length < 10) return;
        len = Number(buf.readBigUInt64BE(2));
        offset = 10;
      }
      const maskLen = masked ? 4 : 0;
      if (buf.length < offset + maskLen + len) return;
      let payload = buf.subarray(offset + maskLen, offset + maskLen + len);
      if (masked) {
        const mask = buf.subarray(offset, offset + 4);
        const out = Buffer.alloc(len);
        for (let i = 0; i < len; i++) out[i] = payload[i] ^ mask[i & 3];
        payload = out;
      }
      buf = buf.subarray(offset + maskLen + len);
      if (opcode === 0x8) { onClose(); return; }
      if (opcode === 0x1) onText(payload.toString('utf8'));
      // opcode 0x9 (ping)/0xA (pong)/0x2 (binary) ignored for our protocol.
    }
  };
}

// ---------------------------------------------------------------------------
// Wire it together
// ---------------------------------------------------------------------------

async function main() {
  await ensureChrome();

  const clients = new Set();

  function broadcastFrame(base64) {
    if (clients.size === 0) return;
    const frame = encodeFrame(Buffer.from(base64, 'base64'), 0x2); // binary
    for (const sock of clients) {
      if (!sock.destroyed) sock.write(frame);
    }
  }
  function broadcastMeta(meta) {
    const frame = encodeFrame(Buffer.from(JSON.stringify({ type: 'meta', ...meta })), 0x1);
    for (const sock of clients) {
      if (!sock.destroyed) sock.write(frame);
    }
  }

  let lastFrameAt = 0;
  const page = new PageSession(
    (b64) => { lastFrameAt = Date.now(); broadcastFrame(b64); },
    broadcastMeta
  );
  await page.attach(null);

  function sendFrameTo(sock, base64) {
    if (!sock.destroyed) sock.write(encodeFrame(Buffer.from(base64, 'base64'), 0x2));
  }

  // Self-healing: relaunch Chrome (if it was closed/crashed) and re-attach the
  // page session. Debounced by `recovering` so overlapping triggers coalesce.
  let recovering = false;
  async function recover() {
    if (recovering || clients.size === 0) return;
    recovering = true;
    try {
      broadcastMeta({ status: 'recovering' });
      log('recovering: (re)launching Chrome + re-attaching...');
      await ensureChrome();
      await page.attach(null);
      lastFrameAt = 0; // force the keepalive to push a fresh frame immediately
      log('recovery complete');
    } catch (e) {
      log('recovery failed, will retry', e.message);
    } finally {
      recovering = false;
    }
  }
  triggerRecover = () => { recover(); };

  // Keepalive: while clients are connected, if the change-driven screencast has
  // been quiet for ~700ms, push a one-shot capture so the canvas stays live
  // even on a static page. A failed capture means Chrome/the page target died,
  // so kick off recovery.
  setInterval(async () => {
    if (clients.size === 0) return;
    if (Date.now() - lastFrameAt < 700) return;
    const shot = await page.captureOnce();
    if (shot) { lastFrameAt = Date.now(); broadcastFrame(shot); }
    else recover();
  }, 700);

  const server = http.createServer((req, res) => {
    if (req.url === '/health') {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ ok: true, targetId: page.targetId }));
      return;
    }
    res.writeHead(426);
    res.end('Upgrade Required');
  });

  server.on('upgrade', (req, socket) => {
    const key = req.headers['sec-websocket-key'];
    if (!key) { socket.destroy(); return; }
    socket.write(
      'HTTP/1.1 101 Switching Protocols\r\n' +
        'Upgrade: websocket\r\n' +
        'Connection: Upgrade\r\n' +
        `Sec-WebSocket-Accept: ${wsAccept(key)}\r\n\r\n`
    );
    socket.setNoDelay(true);
    clients.add(socket);
    log(`dashboard connected (${clients.size} live)`);

    // Push current meta immediately so the new client can size its canvas.
    socket.write(
      encodeFrame(
        Buffer.from(JSON.stringify({ type: 'meta', deviceWidth: page.deviceWidth, deviceHeight: page.deviceHeight, url: null, targetId: page.targetId })),
        0x1
      )
    );
    // Instant first frame: a static page emits no screencast frame until it
    // changes, so capture the current state once for the new client.
    page.captureOnce().then((shot) => { if (shot) sendFrameTo(socket, shot); });

    const decode = makeDecoder(
      async (text) => {
        let m;
        try { m = JSON.parse(text); } catch { return; }
        try {
          switch (m.t) {
            case 'mouse': page.mouse(m.kind, m.fx, m.fy, m.button, m.deltaY); break;
            case 'text': page.text(m.text); break;
            case 'key': page.key(m.kind, m.info); break;
            case 'navigate': page.navigate(m.url); break;
            case 'newtab': await page.newTab(m.url); break;
            case 'targets': {
              const targets = await page.listPageTargets();
              socket.write(encodeFrame(Buffer.from(JSON.stringify({ type: 'targets', targets: targets.map((t) => ({ id: t.id, title: t.title, url: t.url })), current: page.targetId })), 0x1));
              break;
            }
            case 'attach': await page.attach(m.targetId); break;
          }
        } catch (e) {
          log('input error', e.message);
        }
      },
      () => {
        clients.delete(socket);
        socket.destroy();
        log(`dashboard disconnected (${clients.size} live)`);
      }
    );

    socket.on('data', (chunk) => decode(chunk));
    socket.on('close', () => { clients.delete(socket); });
    socket.on('error', () => { clients.delete(socket); socket.destroy(); });
  });

  server.listen(WS_PORT, '127.0.0.1', () => {
    log(`bridge listening on ws://127.0.0.1:${WS_PORT} (start url: ${START_URL})`);
  });
}

main().catch((e) => {
  log('fatal', e);
  process.exit(1);
});
