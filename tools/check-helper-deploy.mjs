// Static gate for the Helper Node deploy artifacts (inert; the fleet manager runs them):
//   deploy/helper/workload-manifest.json  validated against its schema
//   deploy/helper/Dockerfile              non-root, no secrets, no listen default
//   .github/workflows/helper-image.yml    manual dispatch only, read-only default token
// No dependencies, no network, no docker. The helper's own env names are read from
// apps/gateway/internal/helper/config.go so the manifest cannot drift from the binary.

import { readFileSync } from 'node:fs';

const MANIFEST = 'deploy/helper/workload-manifest.json';
const SCHEMA = 'deploy/helper/workload-manifest.schema.json';
const DOCKERFILE = 'deploy/helper/Dockerfile';
const WORKFLOW = '.github/workflows/helper-image.yml';
const CONFIG_GO = 'apps/gateway/internal/helper/config.go';
const failures = [];
const fail = (msg) => failures.push(msg);
const read = (p) => readFileSync(p, 'utf8').replaceAll('\r\n', '\n');
const readJson = (p) => JSON.parse(read(p));

// ---- minimal JSON-schema validator (the subset the schema uses) ----------------
function validate(schema, value, path = '$') {
  const errs = [];
  const type = (v) => (Array.isArray(v) ? 'array' : v === null ? 'null' : Number.isInteger(v) ? 'integer' : typeof v);
  if ('const' in schema && JSON.stringify(schema.const) !== JSON.stringify(value)) {
    errs.push(`${path}: must equal ${JSON.stringify(schema.const)}`);
  }
  if (schema.enum && !schema.enum.some((e) => JSON.stringify(e) === JSON.stringify(value))) {
    errs.push(`${path}: must be one of ${JSON.stringify(schema.enum)}`);
  }
  if (schema.type) {
    const t = type(value);
    if (!(t === schema.type || (schema.type === 'number' && t === 'integer'))) {
      errs.push(`${path}: expected ${schema.type}, got ${t}`);
      return errs;
    }
  }
  if (typeof value === 'string') {
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) errs.push(`${path}: does not match ${schema.pattern}`);
    if (schema.minLength !== undefined && value.length < schema.minLength) errs.push(`${path}: shorter than ${schema.minLength}`);
  }
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) errs.push(`${path}: below ${schema.minimum}`);
    if (schema.maximum !== undefined && value > schema.maximum) errs.push(`${path}: above ${schema.maximum}`);
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems) errs.push(`${path}: fewer than ${schema.minItems} items`);
    if (schema.maxItems !== undefined && value.length > schema.maxItems) errs.push(`${path}: more than ${schema.maxItems} items`);
    if (schema.items) value.forEach((v, i) => errs.push(...validate(schema.items, v, `${path}[${i}]`)));
  }
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const r of schema.required ?? []) if (!(r in value)) errs.push(`${path}: missing ${r}`);
    for (const [k, v] of Object.entries(value)) {
      if (schema.properties?.[k]) errs.push(...validate(schema.properties[k], v, `${path}.${k}`));
      else if (schema.additionalProperties === false) errs.push(`${path}: unexpected property ${k}`);
    }
  }
  return errs;
}

const SECRET_NAME = /(SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|ACCESS_KEY|PRIVATE_KEY|DSN|KEK|CREDENTIAL)/i;
const SECRET_VALUE = /(-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9]{20,}|eyJ[A-Za-z0-9_-]{20,}\.)/;
const WILDCARD = /(^|[^0-9.])0\.0\.0\.0|\[::\]|(^|\s|=|")::(:|"|$|\s)/;

function main() {
  const manifest = readJson(MANIFEST);
  const schema = readJson(SCHEMA);
  const raw = read(MANIFEST);

  // 1. schema
  for (const e of validate(schema, manifest)) fail(`${MANIFEST} ${e}`);

  // 2. no secrets, no wildcard listen, no published ports/limits anywhere in the manifest
  if (SECRET_VALUE.test(raw)) fail(`${MANIFEST}: contains secret-looking material`);
  if (WILDCARD.test(raw)) fail(`${MANIFEST}: contains a wildcard address (0.0.0.0 / ::)`);
  for (const key of ['limits', 'ports', 'publish', 'secrets', 'cpu_limit', 'memory_limit']) {
    if (new RegExp(`"${key}"\\s*:`).test(raw)) fail(`${MANIFEST}: "${key}" is not allowed (requests only; the manager owns limits and ports)`);
  }
  for (const e of manifest.env ?? []) {
    if (SECRET_NAME.test(e.name)) fail(`${MANIFEST}: env ${e.name} looks like a secret`);
  }

  // 3. placeholders: every use declared, every declaration used
  const declared = new Set((manifest.parameters ?? []).map((p) => p.name));
  const used = new Set([...raw.matchAll(/\{\{([a-z_]+)\}\}/g)].map((m) => m[1]));
  for (const u of used) if (!declared.has(u)) fail(`${MANIFEST}: placeholder {{${u}}} is not declared under parameters`);
  for (const d of declared) if (!used.has(d)) fail(`${MANIFEST}: parameter ${d} is declared but never used`);
  for (const p of manifest.parameters ?? []) {
    try {
      new RegExp(p.pattern);
    } catch {
      fail(`${MANIFEST}: parameter ${p.name} has an invalid pattern`);
    }
  }
  if (!/^\{\{image_tag\}\}$/.test(manifest.image?.tag ?? '')) fail(`${MANIFEST}: image.tag must be {{image_tag}}`);

  // 4. bound to the WireGuard address only: the listen entry and UBAG_HELPER_LISTEN agree
  const listen = manifest.network?.listen?.[0];
  const envMap = Object.fromEntries((manifest.env ?? []).map((e) => [e.name, e.value]));
  if (listen && envMap.UBAG_HELPER_LISTEN !== `${listen.address}:${listen.port}`) {
    fail(`${MANIFEST}: UBAG_HELPER_LISTEN must equal network.listen address:port (${listen.address}:${listen.port})`);
  }

  // 5. env names exist in the binary; the required ones are all supplied (manifest or image)
  const goEnv = new Set([...read(CONFIG_GO).matchAll(/"(UBAG_HELPER_[A-Z_0-9]+)"/g)].map((m) => m[1]));
  const docker = read(DOCKERFILE);
  const dockerEnv = dockerEnvNames(docker);
  for (const name of Object.keys(envMap)) if (!goEnv.has(name)) fail(`${MANIFEST}: env ${name} is not read by ${CONFIG_GO}`);
  for (const name of dockerEnv.filter((n) => n.startsWith('UBAG_'))) {
    if (!goEnv.has(name)) fail(`${DOCKERFILE}: ENV ${name} is not read by ${CONFIG_GO}`);
  }
  const supplied = new Set([...Object.keys(envMap), ...dockerEnv]);
  for (const need of [
    'UBAG_HELPER_LISTEN', 'UBAG_HELPER_NODE_ID', 'UBAG_HELPER_CA_FILE', 'UBAG_HELPER_TLS_CERT_FILE',
    'UBAG_HELPER_TLS_KEY_FILE', 'UBAG_HELPER_PRIMARY_URI_SAN', 'UBAG_HELPER_WORKLOAD_VERSION', 'UBAG_HELPER_WORKER_SCRIPT'
  ]) {
    if (!supplied.has(need)) fail(`neither ${MANIFEST} nor ${DOCKERFILE} sets required ${need}`);
  }
  for (const name of [...Object.keys(envMap)]) {
    if (dockerEnv.includes(name)) fail(`${name} is set in both the manifest and the Dockerfile`);
  }

  // 6. mounts cover every file/dir env path; the tls mount is read-only
  const targets = (manifest.mounts ?? []).map((m) => m.target);
  for (const name of ['UBAG_HELPER_CA_FILE', 'UBAG_HELPER_TLS_CERT_FILE', 'UBAG_HELPER_TLS_KEY_FILE']) {
    if (!targets.some((t) => envMap[name]?.startsWith(`${t}/`))) fail(`${MANIFEST}: ${name} is not under any mount target`);
  }
  for (const m of manifest.mounts ?? []) {
    if (m.type === 'bind' && !(m.read_only && /^\{\{[a-z_]+\}\}$/.test(m.source ?? ''))) {
      fail(`${MANIFEST}: bind mount ${m.name} must be read-only with a {{placeholder}} source`);
    }
  }

  // 7. Dockerfile
  if (!/^USER\s+[1-9][0-9]*(:[1-9][0-9]*)?\s*$/m.test(docker)) fail(`${DOCKERFILE}: must end as a numeric non-root USER`);
  if (`${manifest.security?.user}`.split(':')[0] !== (docker.match(/^USER\s+([0-9]+)/m) ?? [])[1]) {
    fail(`${DOCKERFILE}: USER does not match security.user in the manifest`);
  }
  if (!/^ENTRYPOINT\s+\["\/app\/ubag-helper"\]\s*$/m.test(docker)) fail(`${DOCKERFILE}: ENTRYPOINT must be ["/app/ubag-helper"]`);
  if (/^EXPOSE\b/m.test(docker)) fail(`${DOCKERFILE}: EXPOSE is not allowed (the helper binds one WireGuard address)`);
  if (/^ADD\s+https?:/m.test(docker)) fail(`${DOCKERFILE}: ADD from a URL is not allowed`);
  if (/\.(pem|key|crt|env)\b/.test(docker.split('\n').filter((l) => /^(COPY|ADD)\b/.test(l)).join('\n'))) {
    fail(`${DOCKERFILE}: must not COPY certificates, keys or env files`);
  }
  if (SECRET_VALUE.test(docker)) fail(`${DOCKERFILE}: contains secret-looking material`);
  if (WILDCARD.test(docker.split('\n').filter((l) => !/^\s*#/.test(l)).join('\n'))) fail(`${DOCKERFILE}: contains a wildcard address`);
  for (const n of dockerEnv) if (SECRET_NAME.test(n)) fail(`${DOCKERFILE}: ENV ${n} looks like a secret`);
  const home = dockerEnv.includes('HOME') ? (docker.match(/HOME=(\S+)/) ?? [])[1] : undefined;
  if (home && !targets.includes(home)) fail(`${DOCKERFILE}: HOME ${home} must be a writable (tmpfs) mount target in the manifest`);
  const profileRoot = (docker.match(/UBAG_HELPER_PROFILE_ROOT=(\S+)/) ?? [])[1];
  if (profileRoot && !targets.includes(profileRoot)) fail(`${MANIFEST}: no mount at the image's profile root ${profileRoot}`);

  // 8. workflow: manual only, read-only default token, push opt-in
  const wf = read(WORKFLOW);
  const onBlock = (wf.match(/^on:\n((?:[ \t]+.*\n|\n)+)/m) ?? [])[1] ?? '';
  const triggers = [...onBlock.matchAll(/^ {2}([a-z_]+):/gm)].map((m) => m[1]);
  if (triggers.length !== 1 || triggers[0] !== 'workflow_dispatch') {
    fail(`${WORKFLOW}: only workflow_dispatch may trigger it (found: ${triggers.join(', ') || 'none'})`);
  }
  if (!/^permissions:\n {2}contents: read\s*$/m.test(wf)) fail(`${WORKFLOW}: top-level permissions must be contents: read`);
  if (!/^ {6}push:\n(?: {8}[^\n]*\n)*? {8}default: false\n/m.test(wf)) fail(`${WORKFLOW}: the push input must default to false`);
  if (!wf.includes('deploy/helper/Dockerfile')) fail(`${WORKFLOW}: must build deploy/helper/Dockerfile`);
  if (!wf.includes(`${manifest.image?.repository}:sha-`)) fail(`${WORKFLOW}: must tag ${manifest.image?.repository}:sha-<commit>`);
  if (/\bdeploy\b.*\bssh\b|UBAG_DEPLOY_/i.test(wf)) fail(`${WORKFLOW}: must not deploy or use deploy secrets`);

  // 9. wired into test:deployment
  const pkg = readJson('package.json');
  if (!pkg.scripts?.['test:deployment']?.includes('tools/check-helper-deploy.mjs')) {
    fail('package.json: test:deployment must run tools/check-helper-deploy.mjs');
  }
}

function dockerEnvNames(text) {
  const names = [];
  const joined = text.replace(/\\\n/g, ' ');
  for (const line of joined.split('\n')) {
    const m = line.match(/^ENV\s+(.*)$/);
    if (m) for (const kv of m[1].matchAll(/([A-Za-z_][A-Za-z0-9_]*)=/g)) names.push(kv[1]);
  }
  return names;
}

main();
if (failures.length) {
  console.error('helper deploy check failed:');
  for (const f of failures) console.error(`- ${f}`);
  process.exit(1);
}
console.log('helper deploy check passed');
