---
title: Milestone 0 Testing Baseline
description: Documentation-first testing gates for UBAG dashboard, docs, operations, and release workflows.
---

# Milestone 0 Testing Baseline

Milestone 0 testing is a documentation-first contract. It defines the gates future implementation must satisfy before a release is considered ready for promotion.

## Test Layers

- Content checks: frontmatter, links, headings, and broken references.
- Visual checks: docs and dashboard layout across required viewports.
- Accessibility checks: keyboard flow, focus state, labels, contrast, and reduced motion.
- State checks: empty, loading, partial, error, permission, success, and rollback states.
- Observability checks: expected events, logs, traces, and evidence links.
- Release checks: governance checklist, approval, rollback path, and evidence packet.

## Required Viewports

Any docs or dashboard UI work must be checked at:

- 320 px
- 375 px
- 414 px
- 768 px

Pass criteria:

- No page-level horizontal scroll.
- Navigation remains reachable.
- Buttons and links do not wrap into unreadable controls.
- Tables have a mobile alternative or responsive behavior.
- Status text remains visible without relying on color alone.

## Dashboard State Matrix

Each dashboard workflow needs tests for:

- Default state with connected data.
- Empty state with no records.
- Loading state with stable layout.
- Partial state when one source is missing.
- Error state with recovery action.
- Permission-denied state.
- Disabled action state.
- Successful action state.
- Failed action state.

The test should assert copy, accessible names, and next action, not only visual presence.

## Docs Site Checks

Before docs handoff:

- Frontmatter exists on each page.
- Page title is unique.
- Links point to existing docs or clearly labelled placeholders.
- Code fences have language labels when possible.
- No fabricated metrics, testimonials, logos, or claims are introduced.
- Operator and release docs use the same severity and state language.

## Observability Checks

For workflows that emit events:

- Event names follow `domain.resource.action.outcome`.
- Failure and blocked paths emit distinct outcomes.
- Logs do not include secrets or unnecessary personal data.
- Correlation identifiers are preserved across boundaries when available.
- Release evidence links back to the test run or artifact.

## Accessibility Checks

Minimum acceptance:

- Keyboard-only user can reach and activate primary actions.
- Focus-visible styling is present and not animated.
- Icon buttons have accessible names.
- Errors are announced or associated with their controls.
- Reduced-motion mode removes spatial animation.
- Status indicators include text.

## Release Evidence Packet

Every release candidate should collect:

- Test suite or manual checklist name.
- Runner or operator.
- Environment.
- Date and time.
- Result.
- Known gaps.
- Evidence links.
- Approval owner.

If automation is not available yet, a manual checklist is acceptable only when each result is recorded.

## Milestone 0 Acceptance

Testing baseline is accepted when:

- Required viewport gates are documented.
- Dashboard state matrix is explicit.
- Observability and release evidence expectations are defined.
- The docs can be used by implementation owners without relying on unstated assumptions.
