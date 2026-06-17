---
title: Repository Structure
description: Current monorepo layout for UBAG after the v0 edge foundation implementation.
---

## Current layout

```text
apps/dashboard/
apps/docs/
apps/gateway/
apps/worker/
adapters/
deploy/small/
migrations/sqlite/
packages/cli/
packages/conformance/
packages/edge-store/
packages/observability/
packages/openapi/
packages/proto/
packages/sdk-go/
packages/sdk-typescript/
packages/security/
packages/shared-schemas/
PRD.md
PROGRESS.md
IMPLEMENTATION_COVERAGE.md
package.json
pnpm-workspace.yaml
tools/
```

## Implemented Product Layout

```text
apps/
  dashboard/
  gateway/
  worker/
  docs/
packages/
  cli/
  conformance/
  edge-store/
  observability/
  proto/
  openapi/
  shared-schemas/
  sdk-typescript/
  sdk-go/
  security/
adapters/
deploy/
migrations/
tests/
tools/
```

The remaining production-only pieces, such as live provider sessions and deployed compliance modes, are activated through external accounts, deployment infrastructure, and operator-approved secrets rather than committed into this repo.
