#!/usr/bin/env node
// Offline structural validator for the multi-region deployment artifacts under
// deploy/multi-region/{postgres,nats,geodns}.
//
// Checks:
//   Task 3.2 — pgactive bidirectional replication config + conflict policy
//   Task 3.3 — NATS leaf nodes + mTLS supercluster (≥3 nodes per region)
//   Task 3.4 — GeoDNS / anycast edge config
//
// Usage: node tools/check-multiregion.mjs

import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

const passes = [];
const failures = [];

function pass(label) {
  passes.push(label);
  console.log(`  PASS  ${label}`);
}

function fail(label, reason) {
  failures.push({ label, reason });
  console.error(`  FAIL  ${label}`);
  console.error(`        ${reason}`);
}

function fileExists(rel) {
  return existsSync(join(root, rel));
}

function read(rel) {
  return readFileSync(join(root, rel), "utf8");
}

function checkExists(rel, label) {
  if (fileExists(rel)) {
    pass(label ?? `file exists: ${rel}`);
    return true;
  }
  fail(label ?? `file exists: ${rel}`, `missing required file: ${rel}`);
  return false;
}

function checkContains(rel, pattern, label) {
  if (!fileExists(rel)) {
    fail(label, `cannot check content — file missing: ${rel}`);
    return;
  }
  const text = read(rel);
  const found =
    pattern instanceof RegExp ? pattern.test(text) : text.includes(pattern);
  if (found) {
    pass(label);
  } else {
    fail(
      label,
      `${rel} does not contain expected pattern: ${pattern.toString()}`
    );
  }
}

// ---------------------------------------------------------------------------
// Task 3.2 — pgactive bidirectional replication
// ---------------------------------------------------------------------------
console.log("\nTask 3.2 — pgactive bidirectional replication");

checkExists(
  "deploy/multi-region/postgres/pgactive.md",
  "pgactive.md exists"
);

checkContains(
  "deploy/multi-region/postgres/pgactive.md",
  /write-fence|WRITE_FENCE/i,
  "pgactive.md references write-fence policy"
);

checkContains(
  "deploy/multi-region/postgres/pgactive.md",
  "home_region",
  "pgactive.md references home_region"
);

checkExists(
  "deploy/multi-region/postgres/docker-compose.pgactive.yml",
  "docker-compose.pgactive.yml exists"
);

checkContains(
  "deploy/multi-region/postgres/docker-compose.pgactive.yml",
  "pgactive",
  "docker-compose.pgactive.yml references pgactive"
);

checkContains(
  "deploy/multi-region/postgres/docker-compose.pgactive.yml",
  "UBAG_POSTGRES_WRITE_REGION",
  "docker-compose.pgactive.yml documents UBAG_POSTGRES_WRITE_REGION env var"
);

// ---------------------------------------------------------------------------
// Task 3.3 — NATS leaf nodes + mTLS supercluster
// ---------------------------------------------------------------------------
console.log("\nTask 3.3 — NATS leaf nodes + mTLS supercluster");

checkExists(
  "deploy/multi-region/nats/leaf-node.conf",
  "leaf-node.conf exists"
);

checkContains(
  "deploy/multi-region/nats/leaf-node.conf",
  "leafnodes",
  "leaf-node.conf contains leafnodes block"
);

checkContains(
  "deploy/multi-region/nats/leaf-node.conf",
  "tls",
  "leaf-node.conf configures mTLS"
);

checkExists(
  "deploy/multi-region/nats/nats-a-1.conf",
  "nats-a-1.conf exists (additional region-A cluster node)"
);

checkExists(
  "deploy/multi-region/nats/nats-a-2.conf",
  "nats-a-2.conf exists (additional region-A cluster node)"
);

checkExists(
  "deploy/multi-region/nats/nats-b-1.conf",
  "nats-b-1.conf exists (additional region-B cluster node)"
);

checkExists(
  "deploy/multi-region/nats/nats-b-2.conf",
  "nats-b-2.conf exists (additional region-B cluster node)"
);

// nats-a.conf: mTLS on gateway block
checkContains(
  "deploy/multi-region/nats/nats-a.conf",
  "tls",
  "nats-a.conf gateway block contains tls (mTLS)"
);

// nats-a.conf: routes to extra cluster nodes
checkContains(
  "deploy/multi-region/nats/nats-a.conf",
  "nats-a-1",
  "nats-a.conf routes to nats-a-1"
);

checkContains(
  "deploy/multi-region/nats/nats-a.conf",
  "nats-a-2",
  "nats-a.conf routes to nats-a-2"
);

// nats-b.conf: mTLS on gateway block
checkContains(
  "deploy/multi-region/nats/nats-b.conf",
  "tls",
  "nats-b.conf gateway block contains tls (mTLS)"
);

checkContains(
  "deploy/multi-region/nats/nats-b.conf",
  "nats-b-1",
  "nats-b.conf routes to nats-b-1"
);

checkContains(
  "deploy/multi-region/nats/nats-b.conf",
  "nats-b-2",
  "nats-b.conf routes to nats-b-2"
);

// ---------------------------------------------------------------------------
// Task 3.4 — GeoDNS / anycast edge config
// ---------------------------------------------------------------------------
console.log("\nTask 3.4 — GeoDNS / anycast edge config");

checkExists(
  "deploy/multi-region/geodns/README.md",
  "geodns/README.md exists"
);

checkContains(
  "deploy/multi-region/geodns/README.md",
  "/v1/ready",
  "geodns/README.md references /v1/ready health check"
);

checkContains(
  "deploy/multi-region/geodns/README.md",
  /route53|cloudflare/i,
  "geodns/README.md references Route53 or Cloudflare"
);

checkExists(
  "deploy/multi-region/geodns/route53.tf",
  "route53.tf exists"
);

checkContains(
  "deploy/multi-region/geodns/route53.tf",
  "/v1/ready",
  "route53.tf health check uses /v1/ready"
);

checkContains(
  "deploy/multi-region/geodns/route53.tf",
  "latency_routing_policy",
  "route53.tf uses latency_routing_policy"
);

// ---------------------------------------------------------------------------
// Task 4.2 — Phase 9 Go packages: region, mfa, jitadmin, audit, siem
// ---------------------------------------------------------------------------
console.log("\nTask 4.2 — Phase 9 Go packages");

checkExists(
  "apps/gateway/internal/region/region.go",
  "region/region.go exists"
);

checkExists(
  "apps/gateway/internal/region/router.go",
  "region/router.go exists"
);

checkExists(
  "apps/gateway/internal/region/killswitch.go",
  "region/killswitch.go exists"
);

checkExists(
  "apps/gateway/internal/region/pin.go",
  "region/pin.go exists"
);

checkExists(
  "apps/gateway/internal/mfa/mfa.go",
  "mfa/mfa.go exists"
);

checkExists(
  "apps/gateway/internal/mfa/totp.go",
  "mfa/totp.go exists"
);

checkExists(
  "apps/gateway/internal/mfa/recovery.go",
  "mfa/recovery.go exists"
);

checkExists(
  "apps/gateway/internal/jitadmin/grant.go",
  "jitadmin/grant.go exists"
);

checkExists(
  "apps/gateway/internal/jitadmin/middleware.go",
  "jitadmin/middleware.go exists"
);

checkExists(
  "apps/gateway/internal/audit/audit.go",
  "audit/audit.go exists"
);

checkExists(
  "apps/gateway/internal/siem/exporter.go",
  "siem/exporter.go exists"
);

checkExists(
  "apps/gateway/internal/siem/sink.go",
  "siem/sink.go exists"
);

// ---------------------------------------------------------------------------
// Task 4.2 — Phase 9 ADR and operation docs
// ---------------------------------------------------------------------------
console.log("\nTask 4.2 — Phase 9 ADR and operation docs");

checkExists(
  "apps/docs/src/content/docs/adrs/0013-phase9-multiregion-enterprise.md",
  "ADR 0013 exists"
);

checkContains(
  "apps/docs/src/content/docs/adrs/0013-phase9-multiregion-enterprise.md",
  "ubag.jobs.<region>.<lane>.<jobID>",
  "ADR 0013 documents NATS subject scheme"
);

checkContains(
  "apps/docs/src/content/docs/adrs/0013-phase9-multiregion-enterprise.md",
  "home_region",
  "ADR 0013 documents home_region pin"
);

checkContains(
  "apps/docs/src/content/docs/adrs/0013-phase9-multiregion-enterprise.md",
  /draining/,
  "ADR 0013 documents draining kill-switch state"
);

checkExists(
  "apps/docs/src/content/docs/operations/multi-region.md",
  "multi-region.md operation doc exists"
);

checkContains(
  "apps/docs/src/content/docs/operations/multi-region.md",
  "deploy/multi-region/docker-compose.multiregion.yml",
  "multi-region.md references docker-compose.multiregion.yml"
);

checkContains(
  "apps/docs/src/content/docs/operations/multi-region.md",
  "deploy/multi-region/geodns/",
  "multi-region.md references geodns directory"
);

checkContains(
  "apps/docs/src/content/docs/operations/multi-region.md",
  "deploy/multi-region/garage/",
  "multi-region.md references garage directory"
);

checkExists(
  "apps/docs/src/content/docs/operations/enterprise-auth.md",
  "enterprise-auth.md operation doc exists"
);

checkContains(
  "apps/docs/src/content/docs/operations/enterprise-auth.md",
  "/v1/mfa/enroll",
  "enterprise-auth.md documents MFA enrollment endpoint"
);

checkContains(
  "apps/docs/src/content/docs/operations/enterprise-auth.md",
  "/v1/mfa/verify",
  "enterprise-auth.md documents MFA verify endpoint"
);

checkContains(
  "apps/docs/src/content/docs/operations/enterprise-auth.md",
  "/v1/admin/elevation",
  "enterprise-auth.md documents JIT elevation endpoint"
);

checkContains(
  "apps/docs/src/content/docs/operations/enterprise-auth.md",
  "UBAG_SIEM_SPLUNK",
  "enterprise-auth.md documents SIEM Splunk config"
);

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------
console.log(`\n${"─".repeat(60)}`);
console.log(`check-multiregion: ${passes.length} passed, ${failures.length} failed`);

if (failures.length > 0) {
  console.error("\nFailed checks:");
  for (const { label, reason } of failures) {
    console.error(`  - ${label}: ${reason}`);
  }
  process.exit(1);
}

console.log(
  "check-multiregion: OK (all multi-region artifacts present and consistent)"
);
