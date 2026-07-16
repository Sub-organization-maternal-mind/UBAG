#!/usr/bin/env node
// Serves the adapter-static dist/ output exactly as built, with SPA fallback
// to index.html for any path that doesn't match a file. This exists because
// `vite preview` does NOT serve the adapter's dist/ output for a SvelteKit
// project — it serves Vite's own internal build via SvelteKit's preview
// integration, which bypasses patch-csp.mjs entirely and produces different
// (unpatched) HTML. Local/dev-only tool, not part of the build pipeline.
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, extname, join, normalize, resolve } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const distDir = resolve(dir, '../dist');
const port = Number(process.env.PORT ?? 4179);

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.ico': 'image/x-icon',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
};

async function readIfExists(path) {
  try {
    const s = await stat(path);
    if (s.isFile()) return path;
  } catch {
    // not found
  }
  return null;
}

const server = createServer(async (req, res) => {
  const urlPath = decodeURIComponent(req.url.split('?')[0]);
  const safe = normalize(urlPath).replace(/^(\.\.[/\\])+/, '');
  const candidate = join(distDir, safe);

  let filePath = await readIfExists(candidate);
  if (!filePath && !extname(candidate)) {
    filePath = await readIfExists(join(candidate, 'index.html'));
  }
  if (!filePath) {
    // SPA fallback: unknown path, serve the app shell.
    filePath = join(distDir, 'index.html');
  }

  try {
    const body = await readFile(filePath);
    const type = MIME[extname(filePath)] ?? 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': type });
    res.end(body);
  } catch (err) {
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('Not found');
  }
});

server.listen(port, () => {
  console.log(`Serving ${distDir} at http://localhost:${port}`);
});
