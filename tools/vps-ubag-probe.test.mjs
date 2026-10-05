// Proves the probe prints no non-allowlisted env value. Uses a fake `docker` on PATH.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const probe = join(dirname(fileURLToPath(import.meta.url)), 'vps-ubag-probe.sh');
if (spawnSync('bash', ['-c', 'true']).status !== 0) { console.log('skip: no bash'); process.exit(0); }
assert.equal(spawnSync('bash', ['-n', probe]).status, 0, 'bash -n');

const dir = mkdtempSync(join(tmpdir(), 'probe-'));
const fake = join(dir, 'docker');
writeFileSync(fake, `#!/bin/bash
if [ "$1" = exec ] && [ "$3" = env ]; then
  printf '%s\\n' UBAG_WORKER_DAEMON=true UBAG_WORKER_CONCURRENCY=1 UBAG_VOICE_RELAY_SECRET=SENTINEL_SECRET_1 UBAG_DATABASE_URL=postgres://u:SENTINEL_SECRET_2@h/db GOMAXPROCS=1
fi
if [ "$1" = logs ]; then echo "error SENTINEL_SECRET_3"; fi
`);
chmodSync(fake, 0o755);
const posix = (p) => p.replace(/\\/g, '/').replace(/^([A-Za-z]):/, '/$1');
const r = spawnSync('bash', [probe], { env: { ...process.env, PATH: `${posix(dir)}:${process.env.PATH}` }, encoding: 'utf8' });
const out = r.stdout;
assert.ok(!out.includes('SENTINEL'), 'secret value leaked:\n' + out);
assert.match(out, /UBAG_WORKER_DAEMON = true/);
assert.match(out, /UBAG_VOICE_RELAY_SECRET \(len=17\)/);
assert.match(out, /UBAG_DATABASE_URL \(len=\d+\)/);
console.log('vps-ubag-probe: ok');
