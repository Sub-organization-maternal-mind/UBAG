// Minimal static server for the built dashboard (apps/dashboard/dist).
// Companion to start-local.ps1 — the repo's tools/local-launcher references an
// identical scripts/serve-static.mjs that is absent from this checkout, and
// `vite preview` crashes when the dashboard is rebuilt underneath it (it serves
// .svelte-kit/output files by hash). This serves dist/ directly with SPA
// fallback so any route resolves to index.html.
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), 'apps', 'dashboard', 'dist');
const port = Number(process.env.PORT || 58180);
const gatewayUrl = new URL(process.env.UBAG_DASHBOARD_GATEWAY_URL || 'http://127.0.0.1:58080');
const types = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript',
  '.mjs': 'text/javascript',
  '.css': 'text/css',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.ico': 'image/x-icon',
  '.wasm': 'application/wasm',
  '.map': 'application/json',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
};

// Content types worth gzipping on the fly (text-ish assets).
const compressible = new Set([
  'text/html; charset=utf-8',
  'text/javascript',
  'text/css',
  'application/json',
  'image/svg+xml',
]);

http.createServer(async (req, res) => {
  try {
    const requestUrl = new URL(req.url, 'http://localhost');
    const urlPath = decodeURIComponent(requestUrl.pathname);
    if (urlPath === '/v1' || urlPath.startsWith('/v1/')) {
      const upstream = http.request(new URL(requestUrl.pathname + requestUrl.search, gatewayUrl), {
        method: req.method,
        headers: { ...req.headers, host: gatewayUrl.host },
      }, (response) => {
        res.writeHead(response.statusCode ?? 502, response.headers);
        response.pipe(res);
      });
      upstream.on('error', () => {
        if (res.headersSent) {
          res.destroy();
        } else {
          res.writeHead(502, { 'content-type': 'application/json' });
          res.end(JSON.stringify({ error: 'Gateway unavailable' }));
        }
      });
      res.on('close', () => upstream.destroy());
      req.pipe(upstream);
      return;
    }
    let rel = urlPath.replace(/^\/+/, '');
    if (rel === '') rel = 'index.html';
    let file = path.resolve(root, rel);
    if (!file.startsWith(root)) {
      res.writeHead(403).end();
      return;
    }
    let data;
    try {
      data = await readFile(file);
    } catch {
      file = path.join(root, 'index.html');
      data = await readFile(file);
    }
    // Vite content-hashes everything under _app/immutable — safe to cache
    // forever. Everything else (index.html, sw.js, manifest) revalidates.
    const immutable = urlPath.includes('/_app/immutable/');
    const headers = {
      'content-type': types[path.extname(file).toLowerCase()] || 'application/octet-stream',
      'cache-control': immutable ? 'public, max-age=31536000, immutable' : 'no-cache',
    };
    const acceptsGzip = (req.headers['accept-encoding'] ?? '').includes('gzip');
    if (acceptsGzip && compressible.has(headers['content-type']) && data.length > 1024) {
      headers['content-encoding'] = 'gzip';
      headers['vary'] = 'Accept-Encoding';
      data = gzipSync(data);
    }
    res.writeHead(200, headers);
    res.end(data);
  } catch (err) {
    res.writeHead(500, { 'content-type': 'text/plain' });
    res.end(String(err));
  }
}).listen(port, '127.0.0.1', () => {
  console.log(`UBAG dashboard: http://localhost:${port}`);
});
