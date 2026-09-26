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

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), 'apps', 'dashboard', 'dist');
const port = Number(process.env.PORT || 58180);
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

http.createServer(async (req, res) => {
  try {
    const urlPath = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
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
    res.writeHead(200, {
      'content-type': types[path.extname(file).toLowerCase()] || 'application/octet-stream',
      'cache-control': 'no-cache',
    });
    res.end(data);
  } catch (err) {
    res.writeHead(500, { 'content-type': 'text/plain' });
    res.end(String(err));
  }
}).listen(port, '127.0.0.1', () => {
  console.log(`UBAG dashboard: http://localhost:${port}`);
});
