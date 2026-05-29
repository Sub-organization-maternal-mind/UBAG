import { existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

const goExe = findGo();

if (!goExe) {
  console.error('Go is not available on PATH or local Codex toolchains.');
  process.exit(1);
}

const result = spawnSync(goExe, ['version'], { encoding: 'utf8' });

if (result.status !== 0) {
  process.stderr.write(result.stderr ?? '');
  process.exit(result.status ?? 1);
}

process.stdout.write(result.stdout);

function findGo() {
  const onPath = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (!onPath.error && onPath.status === 0) {
    return 'go';
  }

  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData) return null;

  const root = join(localAppData, 'CodexToolchains');
  if (!existsSync(root)) return null;

  const candidates = readdirSync(root, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith('go'))
    .map((entry) => join(root, entry.name, 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go'))
    .filter((path) => existsSync(path))
    .sort()
    .reverse();

  return candidates[0] ?? null;
}
