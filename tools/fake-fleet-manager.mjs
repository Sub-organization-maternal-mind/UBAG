// Fake OET fleet manager for rehearsing the P4.20 canary drills without the
// real manager (which has no project-facing allocation API yet).
// Serves the allocation_list body of packages/shared-schemas/schemas/node-allocation.schema.json
// at GET /v1/allocations with ETag / If-None-Match, exactly what
// apps/gateway/internal/nodes/allocation_source.go polls.
//
//   node tools/fake-fleet-manager.mjs --node-id canary-1 --endpoint 10.8.0.2:7443 [--port 8099] [--host 127.0.0.1]
//
// Drills are driven by POST /_control (loopback only; a non-loopback --host is refused):
//   {"mode":"down"}            -> 503 on every poll        (manager-down drill)
//   {"mode":"bad"}             -> 200 with a junk body      (fail-closed parse)
//   {"mode":"ok"}              -> normal
//   {"state":"draining"|"revoked"|"active", "generation":N, "max_browser_workloads":N,
//    "valid_for_seconds":N, "node_id":"..."}  -> change the grant (generation auto-bumps)
// Not a production component: no auth, no TLS, no persistence. Never expose it.

import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const loopback = (h) => ['127.0.0.1', '::1', 'localhost'].includes(h);

export function createFakeManager({ nodeId = 'canary-1', endpoint = '127.0.0.1:7443', region = 'eu-1', now = Date.now } = {}) {
  const grant = {
    node_id: nodeId, endpoint, region, state: 'active', generation: 1,
    max_browser_workloads: 1, cpu_millis: 1500, memory_bytes: 2684354560, valid_for_seconds: 3600,
  };
  let mode = 'ok';
  let body, etag;
  const rebuild = () => {
    const t = now();
    const entry = {
      schema_version: 1, node_id: grant.node_id, region: grant.region, endpoint: grant.endpoint,
      cert_identity: { uri_san: `spiffe://ubag/node/${grant.node_id}` },
      cpu_millis: grant.cpu_millis, memory_bytes: grant.memory_bytes, reservation_state: 'known',
      state: grant.state, max_browser_workloads: grant.max_browser_workloads, voice_capable: false,
      valid_until: new Date(t + grant.valid_for_seconds * 1000).toISOString(), generation: grant.generation,
    };
    body = JSON.stringify({ schema_version: 1, generated_at: new Date(t).toISOString(), allocations: [entry] });
    etag = `"${createHash('sha256').update(JSON.stringify(entry)).digest('hex').slice(0, 16)}"`;
  };
  rebuild();

  const server = createServer((req, res) => {
    const send = (code, text, headers = {}) => { res.writeHead(code, { 'content-type': 'application/json', ...headers }); res.end(text); };
    if (req.method === 'POST' && req.url === '/_control') {
      if (!loopback(req.socket.remoteAddress?.replace('::ffff:', '') ?? '')) return send(403, '{}');
      let raw = '';
      req.on('data', (c) => { raw += c; if (raw.length > 4096) req.destroy(); });
      req.on('end', () => {
        let c;
        try { c = JSON.parse(raw); } catch { return send(400, '{"error":"json"}'); }
        if (c.mode !== undefined) { if (!['ok', 'down', 'bad'].includes(c.mode)) return send(400, '{"error":"mode"}'); mode = c.mode; }
        let changed = false;
        for (const k of ['state', 'node_id', 'max_browser_workloads', 'valid_for_seconds', 'generation']) {
          if (c[k] !== undefined) { grant[k] = c[k]; changed = true; }
        }
        if (changed) { if (c.generation === undefined) grant.generation += 1; rebuild(); }
        send(200, JSON.stringify({ mode, generation: grant.generation, state: grant.state }));
      });
      return;
    }
    if (req.method !== 'GET' || req.url !== '/v1/allocations') return send(404, '{}');
    if (mode === 'down') return send(503, '{"error":"manager down"}');
    if (mode === 'bad') return send(200, '{"schema_version":1,"allocations":"nope"}');
    if (req.headers['if-none-match'] === etag) { res.writeHead(304, { etag }); return res.end(); }
    send(200, body, { etag });
  });
  return { server, grant };
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  const arg = (n, d) => { const i = process.argv.indexOf(`--${n}`); return i > 0 ? process.argv[i + 1] : d; };
  const host = arg('host', '127.0.0.1');
  if (!loopback(host)) { console.error('fake-fleet-manager: refusing a non-loopback --host (no auth, rehearsal only)'); process.exit(2); }
  const m = createFakeManager({ nodeId: arg('node-id', 'canary-1'), endpoint: arg('endpoint', '127.0.0.1:7443') });
  m.server.listen(Number(arg('port', '8099')), host, () => console.log(`fake fleet manager on http://${host}:${m.server.address().port}/v1/allocations`));
}
