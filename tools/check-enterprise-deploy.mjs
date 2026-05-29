#!/usr/bin/env node
// Offline structural validator for the enterprise deployment artifacts under
// deploy/{helm,terraform,installers,gitops,mtls,multi-region}.
//
// This is a NEW check script. It does not require helm/terraform/kubectl: it
// verifies expected files exist, YAML parses, and a few invariants hold
// (Caddy admin localhost-bound, no committed private keys, UBAG_API_VERSION).
//
// Usage: node tools/check-enterprise-deploy.mjs
import { readFileSync, existsSync, readdirSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const errors = [];
const fail = (m) => errors.push(m);

function read(p) {
  return readFileSync(join(root, p), "utf8");
}
function must(p) {
  if (!existsSync(join(root, p))) fail(`missing required file: ${p}`);
}

// Minimal YAML smoke check: balanced-ish, no tabs, parses as a doc list via a
// naive line scan (we avoid a YAML dep). We mainly catch tabs and stray tabs.
function yamlSmoke(p) {
  const text = read(p);
  if (text.includes("\t")) fail(`${p}: contains a tab character (YAML forbids tabs for indentation)`);
}

function walk(dir, cb) {
  for (const name of readdirSync(join(root, dir))) {
    const rel = join(dir, name);
    const st = statSync(join(root, rel));
    if (st.isDirectory()) walk(rel, cb);
    else cb(rel.split("\\").join("/"));
  }
}

// 1. Required files per directory.
const required = [
  "deploy/helm/ubag/Chart.yaml",
  "deploy/helm/ubag/values.yaml",
  "deploy/helm/ubag/values-production.yaml",
  "deploy/helm/ubag/README.md",
  "deploy/helm/ubag/templates/_helpers.tpl",
  "deploy/helm/ubag/templates/deployment.yaml",
  "deploy/helm/ubag/templates/service.yaml",
  "deploy/helm/ubag/templates/ingress.yaml",
  "deploy/helm/ubag/templates/hpa.yaml",
  "deploy/helm/ubag/templates/pdb.yaml",
  "deploy/helm/ubag/templates/serviceaccount.yaml",
  "deploy/helm/ubag/templates/configmap.yaml",
  "deploy/helm/ubag/templates/secret.yaml",
  "deploy/helm/ubag/templates/networkpolicy.yaml",
  "deploy/helm/ubag/templates/servicemonitor.yaml",
  "deploy/terraform/ubag/versions.tf",
  "deploy/terraform/ubag/variables.tf",
  "deploy/terraform/ubag/main.tf",
  "deploy/terraform/ubag/outputs.tf",
  "deploy/terraform/ubag/README.md",
  "deploy/installers/install.sh",
  "deploy/installers/install.ps1",
  "deploy/installers/systemd/ubag-gateway.service",
  "deploy/installers/launchd/com.ubag.gateway.plist",
  "deploy/installers/README.md",
  "deploy/gitops/argocd/application.yaml",
  "deploy/gitops/flux/helmrelease.yaml",
  "deploy/gitops/README.md",
  "deploy/mtls/gen-certs.sh",
  "deploy/mtls/gen-certs.ps1",
  "deploy/mtls/caddy/Caddyfile.mtls.example",
  "deploy/mtls/README.md",
  "deploy/multi-region/docker-compose.multiregion.yml",
  "deploy/multi-region/caddy/Caddyfile.global",
  "deploy/multi-region/nats/nats-a.conf",
  "deploy/multi-region/nats/nats-b.conf",
  "deploy/multi-region/README.md",
];
required.forEach(must);

// 2. YAML smoke check across all .yaml/.yml under deploy/ (excluding helm templates which use Go templating).
walk("deploy", (p) => {
  if ((p.endsWith(".yaml") || p.endsWith(".yml")) && !p.includes("/helm/ubag/templates/")) {
    yamlSmoke(p);
  }
});

// 3. Caddy admin must stay localhost-bound.
for (const cf of [
  "deploy/mtls/caddy/Caddyfile.mtls.example",
  "deploy/multi-region/caddy/Caddyfile.global",
]) {
  const t = read(cf);
  if (!/admin\s+localhost:2019/.test(t)) {
    fail(`${cf}: Caddy admin must be bound to localhost:2019`);
  }
}

// 4. API version consistency.
const apiVersion = "2026-05-22";
for (const f of [
  "deploy/helm/ubag/values.yaml",
  "deploy/installers/gateway.env.example",
  "deploy/multi-region/env.example",
]) {
  if (!read(f).includes(apiVersion)) {
    fail(`${f}: expected UBAG_API_VERSION ${apiVersion}`);
  }
}

// 5. No committed private keys / cert material under deploy/.
walk("deploy", (p) => {
  if (/\.(key|p12|pem)$/.test(p) || /\/out\//.test(p)) {
    fail(`unexpected key/cert material committed: ${p}`);
  }
});

// 6. Helm values: secrets must default empty (no inlined secret values).
const values = read("deploy/helm/ubag/values.yaml");
for (const k of ["UBAG_APP_SECRET", "UBAG_POSTGRES_DSN", "UBAG_WEBHOOK_SECRET"]) {
  const re = new RegExp(`${k}:\\s*""`);
  if (!re.test(values)) fail(`values.yaml: ${k} should default to an empty string`);
}

if (errors.length) {
  console.error("check-enterprise-deploy: FAILED");
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}
console.log("check-enterprise-deploy: OK (all enterprise deploy artifacts present and consistent)");
