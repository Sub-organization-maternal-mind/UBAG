// Self-test for the P4.20 canary tooling (loopback only, no Docker): the fake
// manager serves a schema-conformant allocation_list with ETag/304 and can go
// down / bad / draining, and the env-dump scanner flags secrets. Also gates
// that docs/perf-fleet/CANARY.md exists with its drill sections.

import { readFileSync } from 'node:fs';
import { createFakeManager } from './fake-fleet-manager.mjs';
import { scanEnvDump } from './scan-env-dump.mjs';

const failures = [];
const ok = (c, m) => { if (!c) failures.push(m); };

const schema = JSON.parse(readFileSync('packages/shared-schemas/schemas/node-allocation.schema.json', 'utf8'));
function checkEntry(e) {
  const props = schema.properties;
  for (const k of schema.required) ok(k in e, `entry missing ${k}`);
  for (const k of Object.keys(e)) ok(k in props, `entry has unknown field ${k}`);
  ok(new RegExp(props.node_id.pattern).test(e.node_id), 'node_id pattern');
  ok(new RegExp(props.endpoint.pattern).test(e.endpoint), 'endpoint pattern');
  ok(props.state.enum.includes(e.state), 'state enum');
  ok(e.cert_identity.uri_san === `spiffe://ubag/node/${e.node_id}`, 'uri_san must be spiffe://ubag/node/<node_id>');
  ok(!Number.isNaN(Date.parse(e.valid_until)), 'valid_until');
}

const m = createFakeManager({ nodeId: 'canary-1', endpoint: '127.0.0.1:7443' });
await new Promise((r) => m.server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${m.server.address().port}`;
const get = (h = {}) => fetch(`${base}/v1/allocations`, { headers: h });
const control = (b) => fetch(`${base}/_control`, { method: 'POST', body: JSON.stringify(b) }).then((r) => r.json());

let r = await get();
const list = await r.json();
const etag = r.headers.get('etag');
ok(r.status === 200 && etag, '200 with an ETag');
ok(list.schema_version === 1 && list.generated_at && list.allocations.length === 1, 'allocation_list shape');
checkEntry(list.allocations[0]);
ok(list.allocations[0].generation === 1 && list.allocations[0].max_browser_workloads === 1, 'first helper joins at 1 workload');
ok((await get({ 'if-none-match': etag })).status === 304, '304 when the ETag matches');

await control({ mode: 'down' });
ok((await get()).status === 503, 'down mode answers 503');
await control({ mode: 'bad' });
const bad = await (await get()).json();
ok(typeof bad.allocations === 'string', 'bad mode serves a non-conformant body');
await control({ mode: 'ok', state: 'draining' });
r = await get({ 'if-none-match': etag });
const drained = await r.json();
ok(r.status === 200 && drained.allocations[0].state === 'draining' && drained.allocations[0].generation === 2, 'state change bumps generation and ETag');
ok((await fetch(`${base}/_control`, { method: 'POST', body: '{"mode":"x"}' })).status === 400, 'unknown mode rejected');
m.server.close();

ok(scanEnvDump('UBAG_HELPER_LISTEN=10.8.0.2:7443\nUBAG_HELPER_TLS_KEY_FILE=/etc/ubag-helper/tls/node.key\nHOME=/home/ubag').length === 0, 'clean helper env passes');
ok(scanEnvDump('UBAG_API_TOKEN=abc123').length === 1, 'secret-named env flagged');
ok(scanEnvDump('FOO=ghp_' + 'a'.repeat(30)).length === 1, 'token-looking value flagged');
ok(scanEnvDump('DB=postgres://u:pw@h/db').length === 1, 'credentialed URL flagged');
ok(scanEnvDump('UBAG_API_TOKEN=abc123', ['UBAG_API_TOKEN']).length === 0, '--allow honoured');

const doc = readFileSync('docs/perf-fleet/CANARY.md', 'utf8');
for (const h of ['## Preconditions', '## Step 1', '## Drill A', '## Drill B', '## Drill C', '## Drill D', '## Evidence', '## Abort']) ok(doc.includes(h), `CANARY.md missing "${h}"`);

// every metric the runbook names must be one the gateway emits
const emitted = readFileSync('apps/gateway/internal/helpermetrics/helpermetrics.go', 'utf8');
for (const name of new Set(doc.match(/ubag_[a-z_]+_(?:total|seconds|up|state|limit|nodes)\b/g) ?? [])) {
  ok(emitted.includes(name) || name === 'ubag_lease_renew_failures_total', `CANARY.md names unknown metric ${name}`);
}

if (failures.length) { console.error(`check-fleet-canary FAILED:\n  ${failures.join('\n  ')}`); process.exit(1); }
console.log('check-fleet-canary: ok');
