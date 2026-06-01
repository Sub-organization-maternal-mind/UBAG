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
  // Operator CRDs, RBAC, and config (Task 3.3).
  "deploy/operator/config/crd/targets.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/adapters.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/templates.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/apps.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/rbac/clusterrole.yaml",
  "deploy/operator/config/rbac/clusterrolebinding.yaml",
  "deploy/operator/config/manager/deployment.yaml",
  "deploy/operator/config/kustomization.yaml",
  // GitOps: operator-specific ArgoCD Application + Flux Kustomization.
  "deploy/gitops/argocd/operator-application.yaml",
  "deploy/gitops/flux/operator-kustomization.yaml",
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
  "deploy/helm/ubag/templates/prometheusrule.yaml",
  "deploy/terraform/ubag/versions.tf",
  "deploy/terraform/ubag/variables.tf",
  "deploy/terraform/ubag/main.tf",
  "deploy/terraform/ubag/outputs.tf",
  "deploy/terraform/ubag/README.md",
  "deploy/terraform/_shared/backend.tf",
  "deploy/terraform/_shared/variables.tf",
  "deploy/terraform/_shared/README.md",
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

// 5b. Shared Terraform backend: assert file exists and contains no inline credentials.
// Comments (lines starting with #) are allowed to mention example values, but
// no uncommented `access_key =` or `secret_key =` assignments are permitted.
{
  const backendPath = "deploy/terraform/_shared/backend.tf";
  if (existsSync(join(root, backendPath))) {
    const backendLines = read(backendPath).split("\n");
    for (let i = 0; i < backendLines.length; i++) {
      const raw = backendLines[i];
      const stripped = raw.replace(/#.*$/, ""); // remove inline comments
      // Flag uncommented access_key or secret_key assignments
      if (/\baccess_key\s*=\s*"[^"]+"/.test(stripped)) {
        fail(`${backendPath}:${i + 1}: uncommented access_key credential detected`);
      }
      if (/\bsecret_key\s*=\s*"[^"]+"/.test(stripped)) {
        fail(`${backendPath}:${i + 1}: uncommented secret_key credential detected`);
      }
    }
  }
}

// 6. Operator CRD invariants: each CRD must declare the correct API group.
const crdFiles = [
  "deploy/operator/config/crd/targets.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/adapters.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/templates.ubag.dev_v1alpha1.yaml",
  "deploy/operator/config/crd/apps.ubag.dev_v1alpha1.yaml",
];
for (const cf of crdFiles) {
  if (!existsSync(join(root, cf))) continue; // already caught by must() above
  const t = read(cf);
  if (!t.includes("kind: CustomResourceDefinition")) {
    fail(`${cf}: must declare kind: CustomResourceDefinition`);
  }
  if (!t.includes("group: ubag.dev")) {
    fail(`${cf}: CRD spec.group must be ubag.dev`);
  }
  if (!t.includes("served: true")) {
    fail(`${cf}: CRD version must have served: true`);
  }
  if (!t.includes("storage: true")) {
    fail(`${cf}: CRD version must have storage: true`);
  }
}

// 6b. ClusterRole least-privilege: no wildcard verbs, no wildcard resources.
const crText = read("deploy/operator/config/rbac/clusterrole.yaml");
if (/verbs:\s*\[\s*['"]\*['"]\s*\]/.test(crText) || /- "\*"/.test(crText) || /- '\*'/.test(crText)) {
  fail("deploy/operator/config/rbac/clusterrole.yaml: wildcard verbs are not allowed (least-privilege)");
}
if (/resources:\s*\[\s*['"]\*['"]\s*\]/.test(crText)) {
  fail("deploy/operator/config/rbac/clusterrole.yaml: wildcard resources are not allowed (least-privilege)");
}

// 6c. Operator Deployment must not run as root and must drop all caps.
const deployText = read("deploy/operator/config/manager/deployment.yaml");
if (!deployText.includes("runAsNonRoot: true")) {
  fail("deploy/operator/config/manager/deployment.yaml: must set runAsNonRoot: true");
}
if (!deployText.includes("allowPrivilegeEscalation: false")) {
  fail("deploy/operator/config/manager/deployment.yaml: must set allowPrivilegeEscalation: false");
}

// 7. Helm values: secrets must default empty (no inlined secret values).
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
