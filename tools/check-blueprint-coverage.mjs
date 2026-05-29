import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const root = fileURLToPath(new URL('..', import.meta.url));

const required = [
  'PRD.md',
  'PROGRESS.md',
  'apps/docs/astro.config.mjs',
  'apps/docs/src/content/docs/index.md',
  'apps/docs/src/content/docs/documentation-system.md',
  'apps/docs/src/content/docs/blueprint-coverage.md',
  'IMPLEMENTATION_COVERAGE.md',
  'apps/docs/src/content/docs/implementation-coverage.md',
  'apps/docs/src/content/docs/product/scope.md',
  'apps/docs/src/content/docs/product/principles.md',
  'apps/docs/src/content/docs/product/roadmap.md',
  'apps/docs/src/content/docs/architecture/overview.md',
  'apps/docs/src/content/docs/architecture/technology-stack.md',
  'apps/docs/src/content/docs/architecture/repository-structure.md',
  'apps/docs/src/content/docs/architecture/control-plane.md',
  'apps/docs/src/content/docs/contracts/api-protocols.md',
  'apps/docs/src/content/docs/contracts/job-contract.md',
  'apps/docs/src/content/docs/contracts/job-lifecycle.md',
  'apps/docs/src/content/docs/contracts/idempotency.md',
  'apps/docs/src/content/docs/contracts/error-catalog.md',
  'apps/docs/src/content/docs/contracts/webhooks.md',
  'apps/docs/src/content/docs/contracts/sdk-conformance.md',
  'apps/docs/src/content/docs/worker/architecture.md',
  'apps/docs/src/content/docs/worker/baseline.md',
  'apps/docs/src/content/docs/worker/sessions.md',
  'apps/docs/src/content/docs/worker/sessions-novnc.md',
  'apps/docs/src/content/docs/worker/artifacts.md',
  'apps/docs/src/content/docs/worker/artifact-capture.md',
  'apps/docs/src/content/docs/worker/safe-user-owned-automation.md',
  'apps/docs/src/content/docs/adapters/contract.md',
  'apps/docs/src/content/docs/adapters/provider-rollout.md',
  'apps/docs/src/content/docs/adapters/ai-provider-rollout.md',
  'apps/docs/src/content/docs/adapters/drift-detection.md',
  'apps/docs/src/content/docs/data/storage.md',
  'apps/docs/src/content/docs/data/schema.md',
  'apps/docs/src/content/docs/data/queue.md',
  'apps/docs/src/content/docs/deployment/profiles.md',
  'apps/docs/src/content/docs/deployment/small-profile.md',
  'apps/docs/src/content/docs/deployment/migrations.md',
  'apps/docs/src/content/docs/security/model.md',
  'apps/docs/src/content/docs/security/index.md',
  'apps/docs/src/content/docs/security/implementation-contracts.md',
  'apps/docs/src/content/docs/security/rbac-abac.md',
  'apps/docs/src/content/docs/security/audit-secrets.md',
  'apps/docs/src/content/docs/security/browser-login-controls.md',
  'apps/docs/src/content/docs/compliance/modes.md',
  'apps/docs/src/content/docs/compliance/index.md',
  'apps/docs/src/content/docs/compliance/privacy-modes.md',
  'apps/docs/src/content/docs/dashboard/ux.md',
  'apps/docs/src/content/docs/dashboard/ux-baseline.md',
  'apps/docs/src/content/docs/sdk-cli-sidecar.md',
  'apps/docs/src/content/docs/plugins.md',
  'apps/docs/src/content/docs/operations/observability.md',
  'apps/docs/src/content/docs/operations/observability-baseline.md',
  'apps/docs/src/content/docs/operations/docs-site-baseline.md',
  'apps/docs/src/content/docs/operations/runbook.md',
  'apps/docs/src/content/docs/operations/operator-runbook.md',
  'apps/docs/src/content/docs/testing/strategy.md',
  'apps/docs/src/content/docs/testing/milestone-0-baseline.md',
  'apps/docs/src/content/docs/testing/acceptance-gates.md',
  'apps/docs/src/content/docs/release/governance.md',
  'apps/docs/src/content/docs/release/release-governance.md',
  'apps/docs/src/content/docs/adrs/0001-license-posture.md',
  'apps/docs/src/content/docs/adrs/0002-schema-driven-contracts.md',
  'apps/docs/src/content/docs/adrs/0003-idempotency-first.md',
  'apps/docs/src/content/docs/adrs/0004-profile-specific-queues.md',
  'apps/docs/src/content/docs/adrs/0005-user-owned-browser-automation.md',
  'apps/docs/src/content/docs/adrs/0006-docs-first-workflow.md',
  'apps/docs/src/content/docs/adrs/0007-hallmark-najm-design.md'
];

const missing = required.filter((file) => !existsSync(join(root, file)));

if (missing.length > 0) {
  console.error('Missing required UBAG docs:');
  for (const file of missing) console.error(`- ${file}`);
  process.exit(1);
}

const progress = readFileSync(join(root, 'PROGRESS.md'), 'utf8');
const requiredTerms = [
  'Universal command contract',
  'Stable error contract',
  'Idempotency semantics',
  'Browser worker fleet',
  'Built-in adapters',
  'Queue Abstraction',
  'Compliance and privacy',
  'World-class checklist'
];

const missingTerms = requiredTerms.filter((term) => !progress.includes(term));

if (missingTerms.length > 0) {
  console.error('PROGRESS.md is missing blueprint coverage terms:');
  for (const term of missingTerms) console.error(`- ${term}`);
  process.exit(1);
}

console.log(`Blueprint coverage check passed: ${required.length} required docs present.`);
