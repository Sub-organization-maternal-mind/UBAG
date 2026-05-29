import { existsSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const targetDir = resolve(process.argv[2] ?? '.');
const goExe = findGo();

if (!goExe) {
  console.error('Go tests blocked: go is not available on PATH or local Codex toolchains.');
  process.exit(1);
}

const version = spawnSync(goExe, ['version'], { encoding: 'utf8' });
if (version.status !== 0) {
  process.stderr.write(version.stderr ?? '');
  process.exit(version.status ?? 1);
}
process.stdout.write(version.stdout);

const test = spawnSync(goExe, ['test', './...'], {
  cwd: targetDir,
  stdio: 'inherit',
  env: {
    ...process.env,
    GOTOOLCHAIN: 'local'
  }
});

if (test.error) {
  console.error(`Failed to run ${goExe}: ${test.error.message}`);
  process.exit(1);
}

process.exit(test.status ?? 1);

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
