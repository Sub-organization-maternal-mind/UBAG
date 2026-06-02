import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  integrations: [
    starlight({
      title: 'UBAG',
      description:
        'Documentation for the Universal Browser-Automation Gateway platform.',
      customCss: ['./src/styles/custom.css'],
      head: [
        {
          tag: 'link',
          attrs: {
            rel: 'preconnect',
            href: 'https://fonts.googleapis.com'
          }
        },
        {
          tag: 'link',
          attrs: {
            rel: 'preconnect',
            href: 'https://fonts.gstatic.com',
            crossorigin: ''
          }
        },
        {
          tag: 'link',
          attrs: {
            rel: 'stylesheet',
            href: 'https://fonts.googleapis.com/css2?family=Bricolage+Grotesque:wght@500;600;700&family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@500;600&display=swap'
          }
        }
      ],
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/ubag/ubag'
        }
      ],
      sidebar: [
        {
          label: 'Start',
          items: [
            { label: 'Overview', slug: '' },
            { label: 'Documentation System', slug: 'documentation-system' },
            { label: 'Blueprint Coverage', slug: 'blueprint-coverage' },
            { label: 'A-Z Implementation Coverage', slug: 'implementation-coverage' }
          ]
        },
        {
          label: 'Product',
          items: [
            { label: 'Scope', slug: 'product/scope' },
            { label: 'Principles', slug: 'product/principles' },
            { label: 'Roadmap', slug: 'product/roadmap' }
          ]
        },
        {
          label: 'Architecture',
          items: [
            { label: 'Overview', slug: 'architecture/overview' },
            { label: 'Technology Stack', slug: 'architecture/technology-stack' },
            { label: 'Repository Structure', slug: 'architecture/repository-structure' },
            { label: 'Control Plane', slug: 'architecture/control-plane' }
          ]
        },
        {
          label: 'Contracts',
          items: [
            { label: 'API Protocols', slug: 'contracts/api-protocols' },
            { label: 'Job Contract', slug: 'contracts/job-contract' },
            { label: 'Job Lifecycle', slug: 'contracts/job-lifecycle' },
            { label: 'Idempotency', slug: 'contracts/idempotency' },
            { label: 'Error Catalog', slug: 'contracts/error-catalog' },
            { label: 'Webhooks', slug: 'contracts/webhooks' },
            { label: 'SDK Conformance', slug: 'contracts/sdk-conformance' }
          ]
        },
        {
          label: 'Worker And Adapters',
          items: [
            { label: 'Worker Baseline', slug: 'worker/baseline' },
            { label: 'Worker Architecture', slug: 'worker/architecture' },
            { label: 'Sessions', slug: 'worker/sessions' },
            { label: 'Sessions And noVNC', slug: 'worker/sessions-novnc' },
            { label: 'Multi-Tab Orchestration', slug: 'worker/multi-tab-orchestration' },
            { label: 'Cross-Engine And Remote Grids', slug: 'worker/cross-engine-grids' },
            { label: 'Artifacts', slug: 'worker/artifacts' },
            { label: 'Artifact Capture', slug: 'worker/artifact-capture' },
            { label: 'Safe Automation', slug: 'worker/safe-user-owned-automation' },
            { label: 'Adapter Contract', slug: 'adapters/contract' },
            { label: 'Provider Rollout', slug: 'adapters/provider-rollout' },
            { label: 'AI Provider Rollout', slug: 'adapters/ai-provider-rollout' },
            { label: 'Drift Detection', slug: 'adapters/drift-detection' }
          ]
        },
        {
          label: 'Platform',
          items: [
            { label: 'Data Storage', slug: 'data/storage' },
            { label: 'Schema', slug: 'data/schema' },
            { label: 'Enterprise Postgres Persistence', slug: 'data/postgres-persistence' },
            { label: 'Queue Abstraction', slug: 'data/queue' },
            { label: 'Deployment Profiles', slug: 'deployment/profiles' },
            { label: 'Small Compose Profile', slug: 'deployment/small-profile' },
            { label: 'Migrations', slug: 'deployment/migrations' },
            { label: 'SDK CLI Sidecar', slug: 'sdk-cli-sidecar' },
            { label: 'Plugins', slug: 'plugins' }
          ]
        },
        {
          label: 'Operations',
          items: [
            { label: 'Security Baseline', slug: 'security' },
            { label: 'Security Model', slug: 'security/model' },
            { label: 'Security Implementation Contracts', slug: 'security/implementation-contracts' },
            { label: 'RBAC And ABAC', slug: 'security/rbac-abac' },
            { label: 'Audit And Secrets', slug: 'security/audit-secrets' },
            { label: 'Audit Export And Merkle Chain', slug: 'security/audit-export-merkle' },
            { label: 'SSO Sessions And Logout', slug: 'security/sso-sessions' },
            { label: 'Browser Login Controls', slug: 'security/browser-login-controls' },
            { label: 'Compliance Baseline', slug: 'compliance' },
            { label: 'Compliance Modes', slug: 'compliance/modes' },
            { label: 'Privacy Modes', slug: 'compliance/privacy-modes' },
            { label: 'Dashboard UX', slug: 'dashboard/ux' },
            { label: 'Dashboard UX Baseline', slug: 'dashboard/ux-baseline' },
            { label: 'Observability', slug: 'operations/observability' },
            { label: 'Observability Baseline', slug: 'operations/observability-baseline' },
            { label: 'Manual-Action Alerts', slug: 'operations/manual-action-alerts' },
            { label: 'Docs Site Baseline', slug: 'operations/docs-site-baseline' },
            { label: 'Agent Handoff', slug: 'operations/agent-handoff' },
            { label: 'Runbook', slug: 'operations/runbook' },
            { label: 'Operator Runbook', slug: 'operations/operator-runbook' },
            { label: 'Testing Strategy', slug: 'testing/strategy' },
            { label: 'Milestone 0 Testing', slug: 'testing/milestone-0-baseline' },
            { label: 'Acceptance Gates', slug: 'testing/acceptance-gates' },
            { label: 'Release Governance', slug: 'release/governance' },
            { label: 'Release Governance Baseline', slug: 'release/release-governance' }
          ]
        },
        {
          label: 'ADRs',
          items: [{ autogenerate: { directory: 'adrs' } }]
        },
        {
          label: 'API Reference',
          items: [
            { label: 'REST API', slug: 'reference/api' },
            { label: 'Proto Reference', slug: 'reference/proto' }
          ]
        },
        {
          label: 'Cookbook',
          items: [{ autogenerate: { directory: 'cookbook' } }]
        },
        {
          label: 'Guides',
          items: [
            { label: 'App Developer', slug: 'guides/app-developer' },
            { label: 'Adapter Author', slug: 'guides/adapter-author' },
            { label: 'Plugin Author', slug: 'guides/plugin-author' },
            { label: 'Operator', slug: 'guides/operator' },
            { label: 'Security', slug: 'guides/security' },
            { label: 'Contributor', slug: 'guides/contributor' }
          ]
        },
        {
          label: 'Threat Model & Compliance',
          items: [
            { label: 'Threat Model', slug: 'security/threat-model' },
            { label: 'HIPAA Mapping', slug: 'compliance/hipaa' },
            { label: 'GDPR Mapping', slug: 'compliance/gdpr' },
            { label: 'SOC2 Mapping', slug: 'compliance/soc2' }
          ]
        }
      ]
    })
  ]
});
