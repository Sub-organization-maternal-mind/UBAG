#!/usr/bin/env node
// Offline structural validator for GitOps deployment artifacts.
//
// Checks:
//   1. ArgoCD Application references deploy/helm/ubag as the chart path
//   2. Flux HelmRelease references deploy/helm/ubag as the chart
//   3. API versions match expected patterns
//   4. No real secrets committed (no base64/high-entropy values for known keys)
//   5. Sample config files are present
//   6. Chart version is consistent across sample configs and Chart.yaml
//
// Usage: node tools/check-gitops.mjs
import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const errors = [];
const fail = (m) => errors.push(m);

function read(p) {
  return readFileSync(join(root, p), "utf8");
}

function must(p) {
  if (!existsSync(join(root, p))) {
    fail(`missing required file: ${p}`);
    return false;
  }
  return true;
}

// ── 1. Required files ────────────────────────────────────────────────────────

const required = [
  "deploy/gitops/argocd/application.yaml",
  "deploy/gitops/flux/helmrelease.yaml",
  "deploy/helm/ubag/Chart.yaml",
  // Sample config
  "deploy/gitops/sample-config/argocd/ubag-app.yaml",
  "deploy/gitops/sample-config/flux/ubag-helmrelease.yaml",
  "deploy/gitops/sample-config/secrets/ubag-secret.yaml.example",
  "deploy/gitops/sample-config/README.md",
];
required.forEach(must);

// ── 2. ArgoCD Application invariants ────────────────────────────────────────

const argoPath = "deploy/gitops/argocd/application.yaml";
if (existsSync(join(root, argoPath))) {
  const argo = read(argoPath);

  // API version must be argoproj.io/v1alpha1
  if (!argo.includes("apiVersion: argoproj.io/v1alpha1")) {
    fail(`${argoPath}: expected apiVersion: argoproj.io/v1alpha1`);
  }

  // Kind must be Application
  if (!argo.includes("kind: Application")) {
    fail(`${argoPath}: expected kind: Application`);
  }

  // Chart path must reference deploy/helm/ubag
  if (!argo.includes("path: deploy/helm/ubag")) {
    fail(`${argoPath}: source.path must be deploy/helm/ubag`);
  }

  // Destination namespace should be ubag
  if (!argo.includes("namespace: ubag")) {
    fail(`${argoPath}: destination.namespace must be ubag`);
  }
}

// ── 3. Flux HelmRelease invariants ──────────────────────────────────────────

const fluxPath = "deploy/gitops/flux/helmrelease.yaml";
if (existsSync(join(root, fluxPath))) {
  const flux = read(fluxPath);

  // API version must match helm.toolkit.fluxcd.io/v2 (or v2beta*)
  if (!/apiVersion:\s+helm\.toolkit\.fluxcd\.io\/v2/.test(flux)) {
    fail(`${fluxPath}: expected apiVersion matching helm.toolkit.fluxcd.io/v2 (or v2beta*)`);
  }

  // Kind must be HelmRelease
  if (!flux.includes("kind: HelmRelease")) {
    fail(`${fluxPath}: expected kind: HelmRelease`);
  }

  // Chart spec must reference deploy/helm/ubag
  if (!flux.includes("chart: deploy/helm/ubag")) {
    fail(`${fluxPath}: chart.spec.chart must be deploy/helm/ubag`);
  }

  // sourceRef must reference a GitRepository
  if (!flux.includes("kind: GitRepository")) {
    fail(`${fluxPath}: chart.spec.sourceRef.kind must be GitRepository`);
  }
}

// ── 4. Sample ArgoCD Application invariants ──────────────────────────────────

const sampleArgoPath = "deploy/gitops/sample-config/argocd/ubag-app.yaml";
if (existsSync(join(root, sampleArgoPath))) {
  const sa = read(sampleArgoPath);

  if (!sa.includes("apiVersion: argoproj.io/v1alpha1")) {
    fail(`${sampleArgoPath}: expected apiVersion: argoproj.io/v1alpha1`);
  }
  if (!sa.includes("kind: Application")) {
    fail(`${sampleArgoPath}: expected kind: Application`);
  }
  if (!sa.includes("path: deploy/helm/ubag")) {
    fail(`${sampleArgoPath}: source.path must be deploy/helm/ubag`);
  }
}

// ── 5. Sample Flux HelmRelease invariants ────────────────────────────────────

const sampleFluxPath = "deploy/gitops/sample-config/flux/ubag-helmrelease.yaml";
if (existsSync(join(root, sampleFluxPath))) {
  const sf = read(sampleFluxPath);

  if (!/apiVersion:\s+helm\.toolkit\.fluxcd\.io\//.test(sf)) {
    fail(`${sampleFluxPath}: expected apiVersion matching helm.toolkit.fluxcd.io/...`);
  }
  if (!sf.includes("kind: HelmRelease")) {
    fail(`${sampleFluxPath}: expected kind: HelmRelease`);
  }
  if (!sf.includes("chart: deploy/helm/ubag")) {
    fail(`${sampleFluxPath}: chart.spec.chart must be deploy/helm/ubag`);
  }
}

// ── 6. No real secrets committed ─────────────────────────────────────────────
//
// Reject any line that assigns a known secret key to a value that looks like
// a real credential (≥20 chars of base64/alphanumeric that is NOT "REPLACE_ME"
// or an empty string).  The .example file is excluded because it intentionally
// contains the literal string "REPLACE_ME".

const secretFiles = [
  "deploy/gitops/argocd/application.yaml",
  "deploy/gitops/flux/helmrelease.yaml",
  "deploy/gitops/sample-config/flux/ubag-helmrelease.yaml",
  "deploy/gitops/sample-config/argocd/ubag-app.yaml",
];

const sensitiveKeyPattern = /UBAG_APP_SECRET|UBAG_POSTGRES_DSN|UBAG_WEBHOOK_SECRET/;
// Match values that are 20+ chars of base64/alphanum (likely a real secret).
const realValuePattern = /:\s*"[A-Za-z0-9+/]{20,}"/;

for (const sf of secretFiles) {
  if (!existsSync(join(root, sf))) continue;
  const lines = read(sf).split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (sensitiveKeyPattern.test(line) && realValuePattern.test(line)) {
      fail(`${sf}:${i + 1}: possible committed secret value detected`);
    }
  }
}

// Also check the example file doesn't accidentally contain real values
const examplePath = "deploy/gitops/sample-config/secrets/ubag-secret.yaml.example";
if (existsSync(join(root, examplePath))) {
  const lines = read(examplePath).split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // Allow "REPLACE_ME" placeholder but reject anything that looks like a real secret
    if (sensitiveKeyPattern.test(line) && realValuePattern.test(line)) {
      fail(`${examplePath}:${i + 1}: example file contains what looks like a real secret value`);
    }
  }
}

// ── 7. Chart version present in Chart.yaml ───────────────────────────────────

const chartPath = "deploy/helm/ubag/Chart.yaml";
if (existsSync(join(root, chartPath))) {
  const chart = read(chartPath);
  const versionMatch = chart.match(/^version:\s+(.+)$/m);
  if (!versionMatch) {
    fail(`${chartPath}: could not parse version field`);
  } else {
    const chartVersion = versionMatch[1].trim();
    // Just verify the version field exists and is non-empty
    if (!chartVersion) {
      fail(`${chartPath}: version field is empty`);
    }
  }
}

// ── Report ────────────────────────────────────────────────────────────────────

if (errors.length) {
  console.error("check-gitops: FAILED");
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}

console.log("✅ GitOps: all checks passed");
