---
title: Dashboard UX Baseline
description: Milestone 0 baseline for the UBAG dashboard and docs site experience.
---

# Dashboard UX Baseline

Milestone 0 defines the dashboard experience for UBAG operators. The goal is a usable control surface for browser automation, jobs, sessions, targets, templates, and release evidence without turning operational work into a marketing page.

## Design Direction

UBAG uses the locked NAJM/Hallmark direction from `design.md`.

- Surface: warm cream paper with ink text.
- Primary accent: terracotta for the main action path only.
- Supporting accents: saffron and marine used sparingly for status, highlights, and editorial moments.
- Type: geometric display for page titles and brand moments; neutral sans for dense UI; monospace only for operational metadata.
- Texture: zellige, textile, and editorial merchandising references can appear as pattern bands, separators, or empty-state treatments.
- Motion: restrained marquee or short hover feedback only where it improves orientation. Respect reduced motion.

Operational dashboard screens must stay dense, scan-friendly, and predictable. Use the fashion-drop energy in hierarchy, patterning, and editorial rhythm, not in oversized hero blocks or decorative cards.

## Dashboard Information Architecture

The first dashboard pass should expose the minimum working map for Milestone 0:

- Overview: current workspace state, active release lane, open operator items, and latest verification evidence.
- Apps: client app registration, app-secret posture, tenant scope, and integration readiness.
- Targets: provider adapters, mock/generic targets, safe-mode status, and manual-login requirements.
- Jobs: recent submissions, lifecycle state, retry/cancel action state, and result evidence.
- Sessions: browser/noVNC sessions, manual login state, quarantine state, and capture policy.
- Templates: prompt, extraction, normalization, and workflow template readiness.
- Operations: deployments, incidents, observability checks, runbooks, and handoff notes.
- Settings: team roles, environments, integrations, and governance controls.

Do not invent job totals, session counts, provider uptime, cost estimates, testimonials, or partner logos. If the data source is not implemented, render a clearly labelled empty state or a `Not connected` state.

## Core Screen Contract

Every dashboard screen should include:

- A concise page title and one sentence of operational context.
- Primary action in terracotta when the user can safely act.
- Secondary actions as text or outlined controls.
- Status summary with source labels, not anonymous numbers.
- Last updated metadata when data is fetched or synced.
- Empty, loading, partial, error, and permission-denied states.
- A clear path back to the operator runbook when recovery is manual.

Avoid nested cards. Use full-width bands, tables, compact lists, drawers, and detail panels. Cards are reserved for repeated items or framed tools and should keep radius at 8px or less unless a larger retail block is intentionally following the NAJM source pattern.

## State Language

Use direct, factual labels:

- `Ready`: the required source is connected and current.
- `Needs review`: data exists but a human decision is required.
- `Blocked`: a dependency is missing or failed.
- `Not connected`: the source has no configured integration.
- `Draft`: content exists but is not eligible for release.
- `Archived`: retained for audit, not active work.

Each state needs an action or explanation. A status badge without a next step is not acceptable for operator workflows.

## Interaction Rules

- Primary actions must have default, hover, focus-visible, active, disabled, loading, error, and success states.
- Focus rings must be visible on warm paper and terracotta surfaces.
- Loading states should preserve layout and avoid shifting tables or lists.
- Use optimistic updates only when rollback or undo is available.
- Confirmation dialogs are reserved for destructive actions.
- Tooltips may clarify compact icons, but critical information must be visible without hover.

## Responsive Gates

Dashboard and docs output must be checked at these widths before handoff:

- 320 px
- 375 px
- 414 px
- 768 px

At each width, confirm there is no horizontal page scroll, primary controls remain reachable, table alternatives are usable, and copy does not collide with adjacent content.

## Accessibility Baseline

- Every interactive icon needs an accessible name.
- Status color must be paired with text.
- Form errors must be connected to their fields.
- Tables need meaningful headers and row actions.
- Keyboard users must be able to complete the primary workflow.
- Reduced-motion users must receive instant or short opacity-only state changes.

## Milestone 0 Acceptance

This baseline is accepted when:

- Dashboard sections have documented intent, state language, and empty-state behavior.
- The NAJM/Hallmark direction is reflected without fabricated commercial proof.
- Mobile gates are listed for implementation verification.
- Operator recovery points link back to runbook-owned documentation.
- Release and testing docs define the evidence required before build work proceeds.
