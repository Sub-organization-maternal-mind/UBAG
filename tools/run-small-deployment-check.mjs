import { spawnSync } from 'node:child_process';

const shell = findPowerShell();
if (!shell) {
  console.error('Small deployment check requires pwsh or powershell on PATH.');
  process.exit(1);
}

const runs = [
  ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'deploy/small/small.ps1', '-Action', 'config', '-UseExampleEnv'],
  ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', 'deploy/small/small.ps1', '-Action', 'config', '-UseExampleEnv', '-Profile', 'observability,queue,smoke']
];

if (hasDockerCompose()) {
  for (const args of runs) {
    const result = spawnSync(shell, args, { stdio: 'inherit' });
    if (result.error) {
      console.error(`Failed to run ${shell}: ${result.error.message}`);
      process.exit(1);
    }
    if (result.status !== 0) {
      process.exit(result.status ?? 1);
    }
  }
} else {
  console.warn('Docker Compose is not available; skipping compose config render and running static deployment checks.');
}

const check = spawnSync(process.execPath, ['tools/check-small-deployment.mjs'], { stdio: 'inherit' });
if (check.error) {
  console.error(`Failed to run deployment checker: ${check.error.message}`);
  process.exit(1);
}
process.exit(check.status ?? 1);

function findPowerShell() {
  for (const candidate of ['pwsh', 'powershell']) {
    const result = spawnSync(candidate, ['-NoProfile', '-Command', '$PSVersionTable.PSVersion.ToString()'], {
      encoding: 'utf8'
    });
    if (!result.error && result.status === 0) return candidate;
  }
  return null;
}

function hasDockerCompose() {
  const result = spawnSync('docker', ['compose', 'version'], { encoding: 'utf8' });
  return !result.error && result.status === 0;
}
