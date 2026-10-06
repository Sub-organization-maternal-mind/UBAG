import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { checkFlags, collectCodeTokens } from './flag-graduation-check.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const read = (p) => readFileSync(join(root, p), 'utf8');
const base = () => ({
  flagsMd: read('docs/perf-fleet/FLAGS.md'),
  rolloutMd: read('docs/perf-fleet/ROLLOUT.md'),
  envExample: read('deploy/vps/env.example'),
  composeText: read('docker-compose.vps.yml'),
  readFile: (p) => { try { return read(p); } catch { return null; } },
  codeTokens: collectCodeTokens(root),
});
const problems = (over) => checkFlags({ ...base(), ...over }).failures;

test('the repository is consistent', () => assert.deepEqual(problems({}), []));

test('a missing checklist step is reported', () => {
  const md = base().rolloutMd.replace(/^- \*\*Rollback:\*\*.*$/m, '');
  assert.ok(problems({ rolloutMd: md }).some((f) => /lacks a non-empty "Rollback"/.test(f)));
});

test('a compose default that turns an inert flag on is reported', () => {
  const c = base().composeText.replace('UBAG_WORKER_STRICT_SUBMIT: ${UBAG_WORKER_STRICT_SUBMIT:-}', 'UBAG_WORKER_STRICT_SUBMIT: ${UBAG_WORKER_STRICT_SUBMIT:-true}');
  assert.ok(problems({ composeText: c }).some((f) => /defaults it on/.test(f)));
});

test('an unknown flag name in the docs is reported', () => {
  assert.ok(problems({ rolloutMd: base().rolloutMd + '\nUBAG_NO_SUCH_FLAG\n' }).some((f) => /UBAG_NO_SUCH_FLAG/.test(f)));
});

test('a taxonomy flag dropped from the inventory is reported', () => {
  const md = base().flagsMd.split('\n').filter((l) => !l.startsWith('| `UBAG_EVENT_NOTIFY`')).join('\n');
  assert.ok(problems({ flagsMd: md }).some((f) => /UBAG_EVENT_NOTIFY.*taxonomy/.test(f)));
});
