import { createServer } from 'node:http';
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { extname, join, normalize, resolve, sep } from 'node:path';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const appRoot = resolve(scriptDir, '..');

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  args.set(process.argv[index], process.argv[index + 1]);
}

const host = args.get('--host') || '127.0.0.1';
const port = Number(args.get('--port') || 4177);
const rootName = args.get('--root') || 'src';
const rootDir = resolve(appRoot, rootName);

if (!rootDir.startsWith(appRoot)) {
  throw new Error(`Refusing to serve unexpected path: ${rootDir}`);
}

const mimeTypes = new Map([
  ['.html', 'text/html; charset=utf-8'],
  ['.css', 'text/css; charset=utf-8'],
  ['.js', 'text/javascript; charset=utf-8'],
  ['.json', 'application/json; charset=utf-8'],
  ['.svg', 'image/svg+xml']
]);
const securityHeaders = {
  'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'X-Frame-Options': 'DENY'
};

const server = createServer(async (request, response) => {
  const requestUrl = new URL(request.url || '/', `http://${host}:${port}`);
  const cleanPath = normalize(decodeURIComponent(requestUrl.pathname)).replace(
    /^(\.\.[/\\])+/,
    ''
  );
  const filePath = resolve(
    rootDir,
    cleanPath === sep || cleanPath === '/' ? 'index.html' : cleanPath.slice(1)
  );

  if (!filePath.startsWith(rootDir)) {
    response.writeHead(403);
    response.end('Forbidden');
    return;
  }

  try {
    const fileStat = await stat(filePath);
    const resolvedFile = fileStat.isDirectory() ? join(filePath, 'index.html') : filePath;
    const ext = extname(resolvedFile);

    response.writeHead(200, {
      ...securityHeaders,
      'content-type': mimeTypes.get(ext) || 'application/octet-stream'
    });
    createReadStream(resolvedFile).pipe(response);
  } catch {
    response.writeHead(404, { ...securityHeaders, 'content-type': 'text/plain; charset=utf-8' });
    response.end('Not found');
  }
});

server.listen(port, host, () => {
  console.log(`UBAG dashboard serving ${rootName} at http://${host}:${port}`);
});
