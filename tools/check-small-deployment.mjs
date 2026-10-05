import { existsSync, readFileSync } from 'node:fs';

const failures = [];

function read(path) {
  return readFileSync(path, 'utf8');
}

function requireTerms(path, terms) {
  const content = read(path);
  for (const term of terms) {
    if (!content.includes(term)) {
      failures.push(`${path} missing ${term}`);
    }
  }
}

requireTerms('docker-compose.small.yml', [
  'UBAG_NATS_URL',
  'UBAG_NATS_STREAM',
  'UBAG_NATS_SUBJECT',
  'UBAG_NATS_WORKER_DURABLE',
  'UBAG_NATS_WORKER_ACK_WAIT_MS',
  'UBAG_NATS_WORKER_NAK_DELAY_MS',
  'UBAG_NATS_WORKER_FETCH_WAIT_MS',
  'UBAG_NATS_WORKER_MAX_DELIVER',
  'UBAG_ARTIFACT_STORE',
  'UBAG_MINIO_ENDPOINT',
  'UBAG_MINIO_ACCESS_KEY',
  'UBAG_MINIO_SECRET_KEY',
  'UBAG_MINIO_BUCKET',
  'UBAG_MINIO_USE_SSL',
  'UBAG_WEBHOOK_OUTBOX',
  'UBAG_WEBHOOK_WORKER_ENABLED',
  'UBAG_WEBHOOK_SECRET',
  'UBAG_WEBHOOK_MAX_ATTEMPTS',
  'UBAG_WEBHOOK_ALLOWED_HOSTS',
  'UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST',
  'nginx-dashboard',
  'UBAG_NGINX_HTTP_PORT',
  'nginx-dashboard/default.conf.template',
  'postgres-migrate',
  'minio-init',
  'ubag-artifacts-rw',
  '--ignore-existing',
  '0002_artifact_metadata.sql',
  '0003_webhook_outbox.sql'
]);

// Live-browser (noVNC) viewer wiring — opt-in "live-browser" profile.
requireTerms('docker-compose.small.yml', [
  'browser-viewer',
  'live-browser',
  'UBAG_BROWSER_VNC_PASSWORD',
  'UBAG_NOVNC_BASE_URL',
  'UBAG_REMOTE_BROWSER_ENDPOINT',
  'browser-topology-register',
  'browser-topology-sync',
  'register-browser-topology.sh',
  'sync-browser-topology.sh',
  'UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS',
  'browser_profiles',
  'deploy/small/browser-viewer/Dockerfile'
]);

requireTerms('deploy/small/browser-viewer/Dockerfile', [
  'google-chrome-stable',
  'x11vnc',
  'novnc',
  'websockify',
  'xvfb'
]);

requireTerms('deploy/small/browser-viewer/entrypoint.sh', [
  'UBAG_BROWSER_VNC_PASSWORD',
  'Xvfb',
  'remote-debugging-port',
  'websockify'
]);

requireTerms('deploy/small/nginx-dashboard/default.conf.template', [
  'upstream ubag_gateway',
  'resolver 127.0.0.11',
  'set                $ubag_browser_viewer http://browser-viewer:6080',
  'auth_basic "UBAG Operator"',
  'proxy_set_header   Authorization',
  'location /novnc/',
  'X-Frame-Options        "SAMEORIGIN"',
  'location /dashboard/'
]);

requireTerms('deploy/small/env.example', [
  'UBAG_EXECUTOR_MODE=noop',
  'UBAG_NGINX_HTTP_PORT=8083',
  'UBAG_NATS_URL=nats://nats:4222',
  'UBAG_NATS_WORKER_DURABLE=ubag-worker',
  'UBAG_NATS_WORKER_MAX_DELIVER=5',
  'UBAG_ARTIFACT_STORE=memory',
  'UBAG_MINIO_ENDPOINT=minio:9000',
  'UBAG_PUBLIC_DOMAIN=ubag.example.com',
  'UBAG_MINIO_ACCESS_KEY=ubag-gateway',
  'UBAG_MINIO_SECRET_KEY=replace-with-local-minio-gateway-password',
  'MINIO_ROOT_USER=ubag-root',
  'MINIO_ROOT_PASSWORD=replace-with-local-minio-root-password',
  'UBAG_WEBHOOK_OUTBOX=memory',
  'UBAG_WEBHOOK_WORKER_ENABLED=false',
  'UBAG_WEBHOOK_SECRET=replace-with-local-webhook-secret',
  'UBAG_WEBHOOK_MAX_ATTEMPTS=8',
  'UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=false',
  'UBAG_WORKER_MAX_RUNTIME_MS=120000',
  'UBAG_BROWSER_VNC_PASSWORD=replace-with-local-vnc-password',
  'UBAG_NOVNC_BASE_URL=http://127.0.0.1:7900',
  'UBAG_REMOTE_BROWSER_ENDPOINT=http://172.31.0.5:9223',
  'UBAG_BROWSER_PRIVATE_IP=172.31.0.5',
  'UBAG_TOPOLOGY_TENANT_ID=tenant_edge',
  'UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS=60'
]);

requireTerms('deploy/small/register-browser-topology.sh', [
  'gateway_browser_instances',
  'gateway_provider_contexts',
  'gateway_browser_tabs',
  'chatgpt_web',
  'gemini_web',
  'deepseek_web',
  'duckai_web'
]);

requireTerms('deploy/small/sync-browser-topology.sh', [
  'UBAG_TOPOLOGY_SYNC_INTERVAL_SECONDS',
  'register-browser-topology.sh',
  'while :'
]);

requireTerms('deploy/small/small.ps1', [
  '-UseExampleEnv is only supported with -Action config.',
  'UBAG_EXECUTOR_MODE=nats requires the queue profile',
  'UBAG_NATS_WORKER_MAX_DELIVER',
  'UBAG_ARTIFACT_STORE=minio',
  'UBAG_MINIO_ACCESS_KEY',
  'UBAG_MINIO_SECRET_KEY',
  'migrate',
  'UBAG_WEBHOOK_WORKER_ENABLED=true requires UBAG_WEBHOOK_OUTBOX=postgres',
  'UBAG_WEBHOOK_SECRET',
  'UBAG_WEBHOOK_ALLOWED_HOSTS must list outbound callback hosts',
  'UBAG_WEBHOOK_OUTBOX=postgres requires UBAG_GATEWAY_STORE=postgres',
  '/v1/ready',
  '/v1/health'
]);

requireTerms('deploy/small/README.md', [
  '0002_artifact_metadata.sql',
  '0003_webhook_outbox.sql',
  'nginx-dashboard',
  'minio-init',
  'least-privilege',
  'MINIO_ROOT_USER',
  'migrate',
  'UBAG_EXECUTOR_MODE=nats',
  'UBAG_NATS_WORKER_DURABLE',
  'UBAG_ARTIFACT_STORE=minio',
  'UBAG_WEBHOOK_WORKER_ENABLED=true',
  '/novnc/'
]);

if (failures.length > 0) {
  console.error(failures.join('\n'));
  process.exit(1);
}

// The gateway entrypoint must never start the service on a partially applied
// schema. It previously logged "WARNING - migration ... failed, continuing",
// which made a half-migrated database the expected outcome: psql runs with
// ON_ERROR_STOP=1, so 0008 aborted at its `CREATE EXTENSION` and NONE of its
// tables were created, yet the gateway booted and reported ready. Assert the
// fail-closed contract structurally rather than by string match, so a
// "continue on error" regression is caught even if the wording changes.
const entrypoint = read('deploy/small/gateway-entrypoint.sh');
if (!/ON_ERROR_STOP=1/.test(entrypoint)) {
  failures.push('deploy/small/gateway-entrypoint.sh must run psql with ON_ERROR_STOP=1');
}
if (!/^\s*exit 1\s*$/m.test(entrypoint)) {
  failures.push(
    'deploy/small/gateway-entrypoint.sh must exit non-zero on a failed migration (fail-closed)'
  );
}
if (/migration \$f failed, continuing/i.test(entrypoint)) {
  failures.push(
    'deploy/small/gateway-entrypoint.sh must not continue after a failed migration'
  );
}
// The extension-dependent migration must stay opt-in and must stay listed in
// OPTIONAL_MIGRATIONS, otherwise every managed Postgres without pg_partman
// crash-loops on boot.
if (!/OPTIONAL_MIGRATIONS="[^"]*0008_blueprint_schema\.sql/.test(entrypoint)) {
  failures.push(
    'deploy/small/gateway-entrypoint.sh must list 0008_blueprint_schema.sql in OPTIONAL_MIGRATIONS'
  );
}

// The opt-in flag has to actually reach the container, in every profile that
// builds from deploy/small/gateway.Dockerfile (env.local alone only feeds
// compose interpolation - it does not inject).
for (const compose of [
  'docker-compose.small.yml',
  'docker-compose.vps.yml'
]) {
  if (!read(compose).includes('UBAG_ALLOW_OPTIONAL_MIGRATIONS')) {
    failures.push(`${compose} must pass UBAG_ALLOW_OPTIONAL_MIGRATIONS to the gateway service`);
  }
}

// VPS resource-budget baseline (P0.3): header/README must not drift from the compose values.
requireTerms('docker-compose.vps.yml', [
  'cpus: "${UBAG_GATEWAY_CPUS:-1.00}"',
  'mem_limit: 1300m',
  'GOMAXPROCS: "1"',
  'cpus: "2.0"',
  'mem_limit: 4096m',
  'pids_limit: 1024',
  'UBAG_WORKER_CONCURRENCY: ${UBAG_WORKER_CONCURRENCY:-1}',
  'UBAG_WORKER_DAEMON: ${UBAG_WORKER_DAEMON:-false}',
  'UBAG_EXECUTOR_MODE: file',
  'UBAG_GATEWAY_STORE: postgres',
  'TOTAL........... 3.25 cpu / 5748m'
]);
requireTerms('deploy/vps/README.md', ['**3.25**', '5748m', 'pids_limit 1024']);

if (failures.length > 0) {
  console.error(failures.join('\n'));
  process.exit(1);
}

// ── Portable remote/HTTPS + voice profile ───────────────────────────────────
// The small profile must work on any host: no VPS-only networks/domains, no
// published browser-control ports, voice env on the service that reads it.
const smallCompose = read('docker-compose.small.yml');
const voiceOverlay = read('deploy/small/compose.voice-media.yml');

function serviceBlock(name) {
  const lines = smallCompose.split('\n');
  const start = lines.findIndex((line) => line === `  ${name}:`);
  if (start === -1) return '';
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (/^ {2}[A-Za-z0-9_-]+:\s*$/.test(lines[i]) || /^\S/.test(lines[i])) {
      end = i;
      break;
    }
  }
  return lines.slice(start, end).join('\n');
}

const gatewayBlock = serviceBlock('gateway');
const viewerBlock = serviceBlock('browser-viewer');
const caddyBlock = serviceBlock('caddy');
const coturnBlock = serviceBlock('coturn');

// 1. No published browser-control ports (CDP, CDP proxy, VNC, audio relay).
const forbiddenPublished = new Set(['9222', '9223', '5900', '9099']);
for (const [file, content] of [
  ['docker-compose.small.yml', smallCompose],
  ['deploy/small/compose.voice-media.yml', voiceOverlay]
]) {
  for (const line of content.split('\n')) {
    const match = line.match(/^\s*-\s*"([^"]+)"\s*$/);
    if (!match || !/^[^"]*:\d/.test(match[1])) continue;
    const spec = match[1].replace(/\/(tcp|udp)$/, '');
    const ports = spec.split(/[:-]/).filter((part) => /^\d+$/.test(part));
    if (ports.some((port) => forbiddenPublished.has(port))) {
      failures.push(`${file} publishes a browser-control port: ${line.trim()}`);
    }
  }
}

// 2. Voice settings live on the gateway (which reads them), not browser-viewer.
for (const name of [
  'UBAG_VOICE_STORE', 'UBAG_VOICE_AUDIO_RELAY_ADDR', 'UBAG_VOICE_MAX_SESSIONS_PER_TENANT',
  'UBAG_VOICE_MAX_QUEUED_PER_TENANT', 'UBAG_VOICE_SESSION_TTL_SECONDS', 'UBAG_VOICE_PROVIDER_ACTIVATION',
  'UBAG_VOICE_CDP_ALLOWED_HOSTS', 'UBAG_VOICE_STUN_URLS', 'UBAG_VOICE_TURN_URLS', 'UBAG_VOICE_TURN_SECRET',
  'UBAG_VOICE_NAT_1TO1_IP', 'UBAG_VOICE_MEDIA_PORT_MIN', 'UBAG_VOICE_MEDIA_PORT_MAX', 'UBAG_ALLOWED_ORIGINS',
  'UBAG_ADMISSION_MAX_INFLIGHT_PER_APP', 'UBAG_ADMISSION_MAX_INFLIGHT_PER_TENANT',
  'UBAG_ADMISSION_MAX_INFLIGHT_GLOBAL', 'UBAG_GATEWAY_MAX_INFLIGHT_REQUESTS', 'UBAG_GATEWAY_UPLOAD_MEMORY_BYTES'
]) {
  if (!gatewayBlock.includes(`${name}:`)) failures.push(`gateway service must set ${name}`);
  if (viewerBlock.includes(`${name}:`)) failures.push(`browser-viewer must not set ${name} (the gateway reads it)`);
}
if (!/UBAG_VOICE_STORE:\s*\$\{UBAG_VOICE_STORE:-\$\{UBAG_GATEWAY_STORE:-memory\}\}/.test(gatewayBlock)) {
  failures.push('gateway UBAG_VOICE_STORE must default to UBAG_GATEWAY_STORE (shared store)');
}
for (const name of ['UBAG_VOICE_AUDIO_ENABLED', 'UBAG_VOICE_RELAY_ADDR']) {
  if (!viewerBlock.includes(`${name}:`)) failures.push(`browser-viewer must set ${name}`);
}
for (const [label, block] of [['gateway', gatewayBlock], ['browser-viewer', viewerBlock]]) {
  if (!block.includes('UBAG_VOICE_RELAY_SECRET:')) failures.push(`${label} must receive UBAG_VOICE_RELAY_SECRET`);
}

// 3. The static browser IP is actually assigned (subnet + ipv4_address).
if (!/ipv4_address:\s*\$\{UBAG_BROWSER_PRIVATE_IP/.test(viewerBlock)) {
  failures.push('browser-viewer must pin ipv4_address to UBAG_BROWSER_PRIVATE_IP');
}
if (!/ipam:[\s\S]*subnet:\s*\$\{UBAG_PRIVATE_SUBNET/.test(smallCompose)) {
  failures.push('ubag-private must declare an ipam subnet (UBAG_PRIVATE_SUBNET)');
}
if (/172\.28\.|nginx-proxy-manager|external:\s*true|\/opt\/platform|\/opt\/docker/.test(smallCompose)) {
  failures.push('docker-compose.small.yml must not depend on VPS-only networks, paths, or external networks');
}

// 4. Optional edges: caddy (tls) and coturn (turn), stock images, bounded ports.
if (
  !/profiles:\s*\["tls"\]/.test(caddyBlock) ||
  !/image:\s*\$\{UBAG_CADDY_IMAGE:-caddy:2\}/.test(caddyBlock) ||
  /build:/.test(caddyBlock)
) {
  failures.push('caddy must be a stock caddy:2 service under profile "tls" (no build)');
}
if (
  !/:80"/.test(caddyBlock) ||
  !/:443"/.test(caddyBlock) ||
  (caddyBlock.match(/^\s*-\s*"[^"]*:\d+"\s*$/gm) || []).length !== 2
) {
  failures.push('caddy must publish exactly 80 and 443');
}
if (!/profiles:\s*\["turn"\]/.test(coturnBlock) || !/coturn\/coturn/.test(coturnBlock)) {
  failures.push('coturn must be a coturn/coturn service under profile "turn"');
}
requireTerms('deploy/small/caddy/Caddyfile', [
  '{$UBAG_PUBLIC_DOMAIN}', 'Strict-Transport-Security', 'request_body', 'max_size',
  'reverse_proxy gateway:8080', 'handle /healthz', '/v1/metrics*', 'admin off'
]);
if (/Authorization|header_up/i.test(read('deploy/small/caddy/Caddyfile').replace(/^\s*#.*$/gm, ''))) {
  failures.push('deploy/small/caddy/Caddyfile must not inject or rewrite tokens');
}
requireTerms('deploy/small/coturn/turnserver.conf', [
  'use-auth-secret', 'no-cli', 'fingerprint', 'denied-peer-ip=10.0.0.0-10.255.255.255',
  'denied-peer-ip=172.16.0.0-172.31.255.255', 'denied-peer-ip=192.168.0.0-192.168.255.255'
]);
requireTerms('docker-compose.small.yml', [
  '--static-auth-secret', '--allowed-peer-ip', '--min-port', '--max-port', '--realm'
]);
requireTerms('deploy/small/compose.voice-media.yml', [
  'UBAG_VOICE_MEDIA_PORT_MIN', 'UBAG_VOICE_MEDIA_PORT_MAX', '/udp"', 'gateway:'
]);

// 5. Migrations 0019-0021 (voice + admission) reach Postgres: the gateway
// entrypoint and the migrate profile both apply the whole directory.
for (const prefix of ['0019_voice_sessions', '0020_voice_instance_global', '0021_admission_tokens']) {
  if (!existsSync(`migrations/postgres/${prefix}.sql`)) failures.push(`migrations/postgres/${prefix}.sql missing`);
}
if (!entrypoint.includes('/app/migrations/postgres/*.sql')) {
  failures.push('gateway-entrypoint.sh must apply every migrations/postgres/*.sql');
}
if (!read('deploy/small/gateway.Dockerfile').includes('COPY migrations/postgres /app/migrations/postgres')) {
  failures.push('gateway.Dockerfile must copy the full migrations/postgres directory');
}
if (!serviceBlock('postgres-migrate').includes('/migrations/*.sql')) {
  failures.push('postgres-migrate must apply every migration (0001-0021)');
}

requireTerms('deploy/small/env.example', [
  'UBAG_VOICE_RELAY_SECRET=', 'openssl rand -hex 32', 'UBAG_ALLOWED_ORIGINS=', 'UBAG_VOICE_NAT_1TO1_IP=',
  'UBAG_VOICE_TURN_SECRET=', 'UBAG_PRIVATE_SUBNET=', 'UBAG_ADMISSION_MAX_INFLIGHT_GLOBAL='
]);
requireTerms('deploy/small/README.md', [
  'Remote/HTTPS + voice', '--profile tls', '--profile turn', 'compose.voice-media.yml',
  '0019_voice_sessions.sql', 'Private-network mode'
]);
requireTerms('deploy/small/nginx-dashboard/default.conf.template', [
  'client_max_body_size 1m;', 'client_max_body_size 48m;'
]);

if (failures.length > 0) {
  console.error(failures.join('\n'));
  process.exit(1);
}

console.log('Small deployment NATS/MinIO/webhook checks passed');
