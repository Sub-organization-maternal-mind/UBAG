// Env dump scan for the P4.20 exit gate ("env dump scan shows no secrets").
// Reads KEY=VALUE lines (`docker exec <c> env`, one Env entry per line from
// `docker inspect`, /proc/<pid>/environ with NULs, or a .env file) from a file
// or stdin and fails when a secret-looking name carries a non-path value, or
// any value looks like key material, a token or a credentialed URL. Prints
// names only, never values.
//   docker exec ubag-helper env | node tools/scan-env-dump.mjs [--allow NAME]...
// UBAG_HELPER_*_FILE values are paths (public cert locations), not secrets.

import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const SECRET_NAME = /(SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|ACCESS_KEY|PRIVATE_KEY|DSN|KEK|CREDENTIAL)/i;
const SECRET_VALUE = /(-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9]{20,}|eyJ[A-Za-z0-9_-]{20,}\.|:\/\/[^/\s:@]+:[^/\s@]+@)/;
const PATHLIKE = /^(\/|[A-Za-z]:[\\/])[^\s]*$/;

export function scanEnvDump(text, allow = []) {
  const findings = [];
  for (const line of text.split(/\r?\n|\0/)) {
    const i = line.indexOf('=');
    if (i <= 0) continue;
    const name = line.slice(0, i).trim().replace(/^export\s+/, '');
    const value = line.slice(i + 1).trim().replace(/^["']|["']$/g, '');
    if (allow.includes(name) || value === '') continue;
    if (SECRET_VALUE.test(value)) findings.push(`${name}: value looks like key material or a token`);
    else if (SECRET_NAME.test(name) && !PATHLIKE.test(value)) findings.push(`${name}: secret-looking name carries a value`);
  }
  return findings;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  const args = process.argv.slice(2);
  const allow = [];
  let file;
  for (let i = 0; i < args.length; i++) args[i] === '--allow' ? allow.push(args[++i]) : (file = args[i]);
  const found = scanEnvDump(readFileSync(file ?? 0, 'utf8'), allow);
  if (found.length) { console.error(`env dump scan FAILED:\n  ${found.join('\n  ')}`); process.exit(1); }
  console.log('env dump scan: no secrets found');
}
