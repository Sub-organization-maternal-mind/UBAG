import { cp, mkdir, rm } from 'node:fs/promises';
import { dirname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const appRoot = resolve(scriptDir, '..');
const srcDir = resolve(appRoot, 'src');
const distDir = resolve(appRoot, 'dist');

if (!distDir.startsWith(`${appRoot}${sep}`)) {
  throw new Error(`Refusing to remove unexpected dist path: ${distDir}`);
}

await rm(distDir, { recursive: true, force: true });
await mkdir(distDir, { recursive: true });
await cp(srcDir, distDir, { recursive: true });

console.log(`Dashboard build written to ${distDir}`);
