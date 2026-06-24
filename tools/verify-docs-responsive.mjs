import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { createServer as createTcpServer } from 'node:net';
import { createReadStream, existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { extname, join, normalize, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const chromePath =
  process.env.CHROME_PATH ??
  (process.platform === 'win32'
    ? 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'
    : 'google-chrome');
const userProvidedBaseUrl = process.env.UBAG_DOCS_URL !== undefined;
let baseUrl = process.env.UBAG_DOCS_URL;
const cdpPortBase = process.env.UBAG_DOCS_CDP_PORT_BASE === undefined
  ? null
  : Number.parseInt(process.env.UBAG_DOCS_CDP_PORT_BASE, 10);
const expectedTitle = process.env.UBAG_DOCS_EXPECTED_TITLE ?? 'UBAG Documentation | UBAG';
const expectedH1 = process.env.UBAG_DOCS_EXPECTED_H1 ?? 'UBAG';
const outDir = join(process.cwd(), '.codex', 'test-output', 'docs-responsive');
const docsDist = join(process.cwd(), 'apps', 'docs', 'dist');
const widths = [320, 375, 414, 768, 1440];
const routes = [
  { path: '/', h1: expectedH1, title: expectedTitle, slug: 'home' },
  { path: '/implementation-coverage/', h1: 'A-Z Implementation Coverage', slug: 'implementation-coverage' },
  { path: '/dashboard/ux/', h1: 'Dashboard UX', slug: 'dashboard-ux' },
  { path: '/contracts/job-contract/', h1: 'Job Contract', slug: 'job-contract' },
  { path: '/operations/runbook/', h1: 'Runtime Recovery Runbook', slug: 'runbook' }
];

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

async function waitForJson(url, timeoutMs = 90000) {
  const started = Date.now();
  let lastError;

  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return response.json();
    } catch (error) {
      lastError = error;
    }
    await delay(250);
  }

  throw lastError ?? new Error(`Timed out waiting for ${url}`);
}

async function waitForChromePort(profile, port, timeoutMs = 90000) {
  const started = Date.now();
  let lastError;

  while (Date.now() - started < timeoutMs) {
    try {
      const resolvedPort = port ?? readDevToolsPort(profile);
      if (resolvedPort !== null) {
        await waitForJson(`http://127.0.0.1:${resolvedPort}/json/version`, 1000);
        return resolvedPort;
      }
    } catch (error) {
      lastError = error;
    }
    await delay(250);
  }

  throw lastError ?? new Error(`Timed out waiting for Chrome DevTools endpoint in ${profile}`);
}

function readDevToolsPort(profile) {
  const activePortFile = join(profile, 'DevToolsActivePort');
  if (!existsSync(activePortFile)) return null;
  const [port] = readFileSync(activePortFile, 'utf8').split(/\r?\n/);
  const parsed = Number.parseInt(port, 10);
  return Number.isFinite(parsed) ? parsed : null;
}

async function openPageTarget(port) {
  const targets = await waitForJson(`http://127.0.0.1:${port}/json/list`);
  const pageTarget = targets.find((target) => target.type === 'page' && target.url === 'about:blank') ??
    targets.find((target) => target.type === 'page');
  if (!pageTarget) {
    throw new Error(`No page target available on Chrome DevTools port ${port}`);
  }
  return pageTarget;
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
    if (!message.id) return;
    const request = pending.get(message.id);
    if (!request) return;
    pending.delete(message.id);
    if (message.error) {
      request.reject(new Error(JSON.stringify(message.error)));
    } else {
      request.resolve(message.result);
    }
  });

  const opened = new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });

  return {
    async send(method, params = {}) {
      await opened;
      const id = nextId++;
      const payload = { id, method, params };
      socket.send(JSON.stringify(payload));
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
      });
    },
    close() {
      socket.close();
    }
  };
}

async function ensureDocsServer() {
	if (userProvidedBaseUrl) {
		await waitForHttp(baseUrl, 15000);
		return null;
	}

	const port = await getFreePort();
	baseUrl = `http://127.0.0.1:${port}/`;
	const server = createDocsStaticServer();
	await new Promise((resolveListen, rejectListen) => {
		server.once('error', rejectListen);
		server.listen(port, '127.0.0.1', resolveListen);
	});
  await waitForHttp(baseUrl, 90000);
	return server;
}

async function stopProcessTree(child) {
	if (!child || child.killed) return;
	if (typeof child.close === 'function' && child.pid === undefined) {
		await new Promise((resolveClose) => child.close(resolveClose));
		return;
	}

  if (process.platform === 'win32' && child.pid) {
    await new Promise((resolve) => {
      const taskkill = spawn('taskkill', ['/PID', String(child.pid), '/T', '/F'], {
        stdio: 'ignore',
        windowsHide: true
      });
      taskkill.once('exit', resolve);
      taskkill.once('error', resolve);
    });
    return;
  }

	child.kill();
}

function createDocsStaticServer() {
	return createServer((request, response) => {
		try {
			const requestUrl = new URL(request.url ?? '/', baseUrl);
			const filePath = resolveDocsFile(requestUrl.pathname);
			if (!filePath) {
				response.writeHead(404);
				response.end('Not found');
				return;
			}
			response.writeHead(200, { 'Content-Type': contentTypeFor(filePath) });
			createReadStream(filePath).pipe(response);
		} catch (error) {
			response.writeHead(500);
			response.end(error instanceof Error ? error.message : 'Internal server error');
		}
	});
}

function resolveDocsFile(pathname) {
	const decoded = decodeURIComponent(pathname);
	const relative = decoded === '/' ? 'index.html' : decoded.replace(/^\/+/, '');
	const candidates = [
		join(docsDist, relative),
		join(docsDist, relative, 'index.html')
	];
	for (const candidate of candidates) {
		const normalized = normalize(candidate);
		if (!normalized.startsWith(normalize(docsDist))) continue;
		if (existsSync(normalized) && statSync(normalized).isFile()) {
			return normalized;
		}
	}
	return null;
}

function contentTypeFor(filePath) {
	switch (extname(filePath).toLowerCase()) {
		case '.html':
			return 'text/html; charset=utf-8';
		case '.js':
			return 'text/javascript; charset=utf-8';
		case '.css':
			return 'text/css; charset=utf-8';
		case '.svg':
			return 'image/svg+xml';
		case '.png':
			return 'image/png';
		case '.webp':
			return 'image/webp';
		default:
			return 'application/octet-stream';
	}
}

function routeUrl(routePath) {
  return new URL(routePath, baseUrl).toString();
}

async function readPageState(cdp, expectedWidth, timeoutMs = 30000) {
  const started = Date.now();
  let lastResult;

  while (Date.now() - started < timeoutMs) {
    lastResult = await cdp.send('Runtime.evaluate', {
      returnByValue: true,
      expression: `(() => ({
        width: window.innerWidth,
        href: window.location.href,
        readyState: document.readyState,
        title: document.title,
        h1: document.querySelector('h1')?.textContent?.trim() ?? null,
        navPresent: Boolean(document.querySelector('nav')),
        htmlScrollWidth: document.documentElement.scrollWidth,
        bodyScrollWidth: document.body.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        overflowOk: document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1 &&
          document.body.scrollWidth <= document.documentElement.clientWidth + 1
      }))()`
    });

    const value = lastResult?.result?.value;
    if (
      value &&
      typeof value === 'object' &&
      Math.abs(Number(value.width) - expectedWidth) <= 1 &&
      value.readyState !== 'loading' &&
      typeof value.title === 'string' &&
      typeof value.h1 === 'string'
    ) {
      return value;
    }

    await delay(150);
  }

  throw new Error(`Unable to read docs page state: ${JSON.stringify(lastResult ?? null)}`);
}

async function launchChrome() {
  const port = cdpPortBase === null ? null : cdpPortBase;
  const profile = join(tmpdir(), `ubag-docs-chrome-${Date.now()}`);
  const chromeStderr = [];
  const chrome = spawn(chromePath, [
    '--headless=new',
    '--disable-gpu',
    '--disable-background-networking',
    '--disable-dev-shm-usage',
    '--disable-extensions',
    '--no-first-run',
    '--no-default-browser-check',
    '--remote-debugging-address=127.0.0.1',
    `--remote-debugging-port=${port ?? 0}`,
    `--user-data-dir=${profile}`,
    '--window-size=1440,1200',
    'about:blank'
  ], { stdio: ['ignore', 'ignore', 'pipe'], windowsHide: true });
  chrome.stderr?.on('data', (chunk) => {
    chromeStderr.push(String(chunk));
    if (chromeStderr.join('').length > 4000) chromeStderr.shift();
  });
  let chromeExit;
  const exited = new Promise((resolve) => {
    chrome.once('exit', (code, signal) => {
      chromeExit = { code, signal };
      resolve();
    });
  });

  try {
    let cdp;
    try {
      const resolvedPort = await waitForChromePort(profile, port);
      const pageTarget = await openPageTarget(resolvedPort);
      cdp = connect(pageTarget.webSocketDebuggerUrl);
      await cdp.send('Page.enable');
      await cdp.send('Runtime.enable');
    } catch (error) {
      const stderr = chromeStderr.join('').trim();
      const exitDetails = chromeExit
        ? ` Chrome exited with code ${chromeExit.code ?? 'null'} and signal ${chromeExit.signal ?? 'null'}.`
        : '';
      throw new Error(
        `Chrome DevTools endpoint did not become ready.${exitDetails}` +
          (stderr ? ` Chrome stderr:\n${stderr}` : ''),
        { cause: error }
      );
    }
    return { chrome, cdp, exited, profile };
  } catch (error) {
    if (!chrome.killed) {
      await stopProcessTree(chrome);
    }
    await Promise.race([exited, delay(2000)]);
    try {
      rmSync(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 250 });
    } catch {
      // Chrome can hold profile files briefly on Windows; leftover temp profiles are safe to remove later.
    }
    throw error;
  }
}

async function closeChrome(browser) {
  browser.cdp.close();
  if (!browser.chrome.killed) {
    await stopProcessTree(browser.chrome);
  }
  await Promise.race([browser.exited, delay(2000)]);
  try {
    rmSync(browser.profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 250 });
  } catch {
    // Chrome can hold profile files briefly on Windows; leftover temp profiles are safe to remove later.
  }
}

async function verifyRouteWidth(cdp, route, width) {
  const height = width <= 414 ? 1200 : 1000;
  const url = routeUrl(route.path);
  await waitForHttp(url, 15000);
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width <= 414
  });
  const navigation = await cdp.send('Page.navigate', { url });
  if (navigation.errorText) {
    throw new Error(`Failed to navigate to ${url}: ${navigation.errorText}`);
  }
  const pageState = await readPageState(cdp, width);

  const screenshot = await cdp.send('Page.captureScreenshot', {
    format: 'png',
    captureBeyondViewport: false
  });
  const screenshotPath = join(outDir, `ubag-docs-${route.slug}-${width}.png`);
  writeFileSync(screenshotPath, Buffer.from(screenshot.data, 'base64'));

  return {
    width,
    route: route.path,
    screenshotPath,
    ...pageState
  };
}

const results = [];
const preview = await ensureDocsServer();
let browser;

try {
  browser = await launchChrome();
  for (const route of routes) {
    for (const width of widths) {
      results.push(await verifyRouteWidth(browser.cdp, route, width));
    }
  }

  for (const result of results) {
    const route = routes.find((item) => item.path === result.route);
    if (!route) {
      throw new Error(`Unexpected route result ${result.route}`);
    }
    if ((route.title !== undefined && result.title !== route.title) || result.h1 !== route.h1) {
      console.error(JSON.stringify(result, null, 2));
      throw new Error(`Unexpected docs page content for ${result.route} at ${result.width}px`);
    }

    if (!result.overflowOk) {
      console.error(JSON.stringify(result, null, 2));
      throw new Error(`Horizontal overflow detected at ${result.width}px`);
    }
  }

  console.log(JSON.stringify(results, null, 2));
} finally {
  if (browser) {
    await closeChrome(browser);
  }
  await stopProcessTree(preview);
}
