#!/usr/bin/env node
// Offline structural validator for deploy/caddy/Caddyfile.standard.
//
// Asserts:
//   1. HTTP/3 is enabled  (h3 or protocols h1 h2 h3)
//   2. rate_limit directive is present
//   3. coraza_waf block is present
//   4. Caddy admin is loopback-bound  (localhost:2019 or 127.0.0.1:2019)
//      — NOT :2019 (all-interfaces) or "admin off"
//
// Usage: node tools/check-caddy.mjs
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const errors = [];
const fail = (m) => errors.push(m);

const caddyfile = join(root, "deploy/caddy/Caddyfile.standard");
let text;
try {
  text = readFileSync(caddyfile, "utf8");
} catch (e) {
  console.error(`check-caddy: cannot read ${caddyfile}: ${e.message}`);
  process.exit(1);
}

// 1. HTTP/3 enabled — accept bare "h3" or the full "protocols h1 h2 h3"
if (!/\bh3\b/.test(text)) {
  fail('Caddyfile.standard: HTTP/3 not enabled — expected "h3" or "protocols h1 h2 h3"');
}

// 2. rate_limit directive present
if (!/\brate_limit\b/.test(text)) {
  fail('Caddyfile.standard: rate_limit directive not found');
}

// 3. coraza_waf block present
if (!/\bcoraza_waf\b/.test(text)) {
  fail('Caddyfile.standard: coraza_waf block not found — WAF must be configured');
}

// 4. Admin must be loopback-bound (localhost:2019 or 127.0.0.1:2019).
//    Reject "admin off", "admin :2019" (all interfaces), or missing admin line.
const adminMatch = text.match(/\badmin\s+(\S+)/);
if (!adminMatch) {
  fail('Caddyfile.standard: no "admin" directive found — Caddy admin must be explicitly bound to localhost:2019');
} else {
  const adminAddr = adminMatch[1];
  const isLoopback =
    adminAddr === "localhost:2019" ||
    adminAddr === "127.0.0.1:2019";
  if (!isLoopback) {
    fail(
      `Caddyfile.standard: admin is bound to "${adminAddr}" — must be localhost:2019 or 127.0.0.1:2019 (never :2019 or off)`
    );
  }
}

if (errors.length) {
  console.error("check-caddy: FAILED");
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}

console.log("✅ Caddyfile.standard: all checks passed");
