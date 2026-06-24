import { readFileSync } from 'node:fs';

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
  'chromium',
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
  'deepseek_web'
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

console.log('Small deployment NATS/MinIO/webhook checks passed');
