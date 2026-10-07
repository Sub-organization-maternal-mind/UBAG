---
title: Milestone 0 Testing Baseline
description: Historical testing baseline. Automated tests were removed on 2026-10-08.
---

# Milestone 0 Testing Baseline

This page is kept for the blueprint coverage gate and for history. The automated
test suites it once described were removed on 2026-10-08 (last commit with tests:
tag `pre-strip-tests`). The current gates are the typecheck and lint commands in
[Testing Strategy](/testing/strategy).

Manual checks still apply to docs and dashboard changes: the viewport list below
is the layout reference for any manual review.

## Required Viewports

- 320 px
- 375 px
- 414 px
- 768 px

Pass criteria for a manual review:

- No page-level horizontal scroll.
- Navigation remains reachable.
- Buttons and links do not wrap into unreadable controls.
- Status text remains visible without relying on color alone.

## Release Evidence

Every release candidate records:

- Date and time, runner or operator, and environment.
- Result of `pnpm lint` and `pnpm typecheck`.
- Known gaps, including the absence of automated tests.
- Approval owner.
