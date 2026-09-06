import { spawn } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { createServer as createTcpServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const appRoot = join(scriptDir, '..');
const chromePath =
  process.env.CHROME_PATH ?? 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const userProvidedBaseUrl = process.env.UBAG_DASHBOARD_URL !== undefined;
let baseUrl = process.env.UBAG_DASHBOARD_URL;
const cdpPortBase = process.env.UBAG_DASHBOARD_CDP_PORT_BASE === undefined
  ? null
  : Number.parseInt(process.env.UBAG_DASHBOARD_CDP_PORT_BASE, 10);
const outDir = join(process.cwd(), '.codex', 'test-output', 'dashboard-responsive');
const widths = [320, 375, 414, 768, 1440];

mkdirSync(outDir, { recursive: true });

async function waitForHttp(url, timeoutMs = 15000) {
  const started = Date.now();
  let lastError;
  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`${url} returned ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await delay(200);
  }
  throw lastError ?? new Error(`Timed out waiting for ${url}`);
}

async function waitForJson(url, timeoutMs = 30000) {
  const started = Date.now();
  let lastError;
  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return response.json();
    } catch (error) {
      lastError = error;
    }
    await delay(150);
  }
  throw lastError ?? new Error(`Timed out waiting for ${url}`);
}

async function getFreePort() {
  return new Promise((resolvePort, rejectPort) => {
    const server = createTcpServer();
    server.once('error', rejectPort);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      server.close(() => {
        if (address && typeof address === 'object') {
          resolvePort(address.port);
        } else {
          rejectPort(new Error('Unable to allocate a local port'));
        }
      });
    });
  });
}

function connect(wsUrl) {
  const socket = new WebSocket(wsUrl);
  let nextId = 1;
  const pending = new Map();

  socket.addEventListener('message', (event) => {
    const message = JSON.parse(event.data);
    const request = pending.get(message.id);
    if (!request) return;
    pending.delete(message.id);
    if (message.error) request.reject(new Error(JSON.stringify(message.error)));
    else request.resolve(message.result);
  });

  const opened = new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });

  return {
    async send(method, params = {}) {
      await opened;
      const id = nextId++;
      socket.send(JSON.stringify({ id, method, params }));
      return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
    },
    close() {
      socket.close();
    }
  };
}

async function ensureDashboardServer() {
  if (userProvidedBaseUrl) {
    await waitForHttp(baseUrl, 15000);
    return null;
  }

  const port = await getFreePort();
  baseUrl = `http://127.0.0.1:${port}/`;
  const preview = spawn(
    process.execPath,
    ['scripts/dev.mjs', '--root', 'dist', '--port', String(port)],
    { cwd: appRoot, stdio: 'ignore', windowsHide: true }
  );
  await waitForHttp(baseUrl, 20000);
  return preview;
}

async function stopProcessTree(child) {
  if (!child || child.killed) return;
  if (process.platform !== 'win32') {
    child.kill();
    await new Promise((resolve) => {
      child.once('exit', resolve);
      setTimeout(resolve, 1000);
    });
    return;
  }
  await new Promise((resolve) => {
    const taskkill = spawn('taskkill', ['/PID', String(child.pid), '/T', '/F'], {
      stdio: 'ignore',
      windowsHide: true
    });
    taskkill.once('exit', resolve);
    taskkill.once('error', resolve);
  });
}

async function readDashboardState(cdp, expectedWidth, timeoutMs = 30000) {
  const started = Date.now();
  let lastResult;

  while (Date.now() - started < timeoutMs) {
    lastResult = await cdp.send('Runtime.evaluate', {
      returnByValue: true,
      expression: `(() => ({
        width: window.innerWidth,
        title: document.title,
        h1: document.querySelector('h1')?.textContent?.trim() ?? null,
        tabs: Array.from(document.querySelectorAll('[role="tab"]')).map((tab) => tab.textContent.trim().toLowerCase()),
        htmlScrollWidth: document.documentElement.scrollWidth,
        bodyScrollWidth: document.body.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        overflowOk: document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1 &&
          document.body.scrollWidth <= document.documentElement.clientWidth + 1,
        activePanelVisible: Boolean(document.querySelector('[role="tabpanel"]:not([hidden])')),
        panelChecks: Array.from(document.querySelectorAll('[role="tab"]')).map((tab) => {
          tab.click();
          const panel = document.querySelector('[role="tabpanel"]:not([hidden])');
          return {
            tab: tab.textContent.trim().toLowerCase(),
            visible: Boolean(panel),
            hasRows: Boolean(panel?.querySelector('tr, .metric-card, .work-item, .activity-item, .empty-state')),
            overflowOk: document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1 &&
              document.body.scrollWidth <= document.documentElement.clientWidth + 1
          };
        })
      }))()`
    });

    const value = lastResult?.result?.value;
    if (
      value &&
      typeof value === 'object' &&
      Math.abs(Number(value.width) - expectedWidth) <= 1 &&
      value.title === 'UBAG Dashboard' &&
      value.h1 === 'Operator Dashboard' &&
      Array.isArray(value.tabs) &&
      value.tabs.length > 0 &&
      value.activePanelVisible
    ) {
      return value;
    }

    await delay(150);
  }

  throw new Error(`Unable to read dashboard page state: ${JSON.stringify(lastResult ?? null)}`);
}

async function verifyWidth(width, index) {
  const port = cdpPortBase === null ? await getFreePort() : cdpPortBase + index;
  const profile = join(tmpdir(), `ubag-dashboard-chrome-${width}-${Date.now()}`);
  const height = width <= 414 ? 1300 : 1000;
  const chrome = spawn(chromePath, [
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    `--window-size=${width},${height}`,
    'about:blank'
  ], { stdio: 'ignore' });
  const exited = new Promise((resolve) => chrome.once('exit', resolve));

  try {
    const targets = await waitForJson(`http://127.0.0.1:${port}/json/list`);
    const pageTarget = targets.find((target) => target.type === 'page') ?? targets[0];
    const cdp = connect(pageTarget.webSocketDebuggerUrl);
    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width <= 414
    });
    await cdp.send('Page.navigate', { url: baseUrl });
    const pageState = await readDashboardState(cdp, width);

    const screenshot = await cdp.send('Page.captureScreenshot', {
      format: 'png',
      captureBeyondViewport: false
    });
    const screenshotPath = join(outDir, `ubag-dashboard-${width}.png`);
    writeFileSync(screenshotPath, Buffer.from(screenshot.data, 'base64'));
    cdp.close();

    return {
      screenshotPath,
      ...pageState
    };
  } finally {
    if (!chrome.killed) chrome.kill();
    await Promise.race([exited, delay(2000)]);
    try {
      rmSync(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 250 });
    } catch {
      // Chrome can briefly hold profile files on Windows.
    }
  }
}

const preview = await ensureDashboardServer();
const results = [];

try {
  for (let index = 0; index < widths.length; index += 1) {
    results.push(await verifyWidth(widths[index], index));
  }

  const expectedTabs = ['overview', 'apps', 'targets', 'jobs', 'sessions', 'templates', 'runtime', 'activation'];
  for (const result of results) {
    if (result.title !== 'UBAG Dashboard' || result.h1 !== 'Operator Dashboard') {
      console.error(JSON.stringify(result, null, 2));
      throw new Error(`Unexpected dashboard content at ${result.width}px`);
    }
    for (const tab of expectedTabs) {
      if (!result.tabs.includes(tab)) {
        console.error(JSON.stringify(result, null, 2));
        throw new Error(`Missing ${tab} tab at ${result.width}px`);
      }
    }
    if (!result.overflowOk || !result.activePanelVisible) {
      console.error(JSON.stringify(result, null, 2));
      throw new Error(`Dashboard responsive check failed at ${result.width}px`);
    }
    for (const panel of result.panelChecks) {
      if (!panel.visible || !panel.hasRows || !panel.overflowOk) {
        console.error(JSON.stringify(result, null, 2));
        throw new Error(`Dashboard ${panel.tab} panel responsive check failed at ${result.width}px`);
      }
    }
  }

  console.log(JSON.stringify(results, null, 2));
} finally {
  await stopProcessTree(preview);
}
