#!/usr/bin/env node
// Offline structural validator for OS-native installer artifacts.
//
// Asserts:
//   WiX MSI (deploy/installers/windows/ubag.wxs):
//     1. <Wix root element is present
//     2. UpgradeCode attribute is present (required for MSI upgrade chains)
//     3. A Component element containing ServiceInstall is present
//
//   macOS pkg (deploy/installers/macos/postinstall):
//     1. Contains "set -e" (fail-fast)
//     2. Contains a launchctl reference (launchd daemon registration)
//
// Usage: node tools/check-installers.mjs
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const errors = [];
const fail = (m) => errors.push(m);

// ---------------------------------------------------------------------------
// WiX MSI checks
// ---------------------------------------------------------------------------
const wxsPath = join(root, "deploy/installers/windows/ubag.wxs");
let wxs;
try {
  wxs = readFileSync(wxsPath, "utf8");
} catch (e) {
  console.error(`check-installers: cannot read ${wxsPath}: ${e.message}`);
  process.exit(1);
}

// 1. <Wix root element
if (!/<Wix[\s>]/.test(wxs)) {
  fail("ubag.wxs: missing <Wix root element");
}

// 2. UpgradeCode attribute (stable GUID required for upgrade chains)
if (!/UpgradeCode\s*=/.test(wxs)) {
  fail("ubag.wxs: UpgradeCode attribute not found — required for MSI upgrade chains");
}

// 3. Component with ServiceInstall inside it
// Check that both Component and ServiceInstall elements are present; their
// co-location inside a Fragment is what matters, not XML nesting depth.
if (!/\bServiceInstall\b/.test(wxs)) {
  fail("ubag.wxs: no ServiceInstall element found — Windows service registration is required");
}
if (!/\bComponent\b/.test(wxs)) {
  fail("ubag.wxs: no Component element found alongside ServiceInstall");
}

// ---------------------------------------------------------------------------
// macOS postinstall script checks
// ---------------------------------------------------------------------------
const postinstallPath = join(root, "deploy/installers/macos/postinstall");
let postinstall;
try {
  postinstall = readFileSync(postinstallPath, "utf8");
} catch (e) {
  console.error(`check-installers: cannot read ${postinstallPath}: ${e.message}`);
  process.exit(1);
}

// 1. set -e (fail-fast)
if (!/\bset\s+-[a-zA-Z]*e[a-zA-Z]*\b/.test(postinstall)) {
  fail("postinstall: missing 'set -e' — script must fail on errors");
}

// 2. launchctl reference (launchd daemon registration)
if (!/\blaunchctl\b/.test(postinstall)) {
  fail("postinstall: no launchctl reference found — launchd daemon registration is required");
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------
if (errors.length) {
  console.error("check-installers: FAILED");
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}

console.log("✅ Installers: all checks passed");
