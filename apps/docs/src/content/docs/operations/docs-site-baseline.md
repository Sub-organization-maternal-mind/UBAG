---
title: Docs Site Baseline
description: Milestone 0 authoring and UX baseline for the UBAG documentation site.
---

# Docs Site Baseline

The UBAG docs site is the source of operational truth for Milestone 0. It should explain what is decided, what is pending, what evidence exists, and which owner is accountable for the next action.

## Site Purpose

The docs site should support:

- Implementation handoff.
- Operator recovery.
- Testing evidence.
- Release approval.
- Security, compliance, and governance review.
- Future onboarding for maintainers.

It is not a marketing site. The NAJM/Hallmark direction should appear through typography, warm surfaces, editorial rhythm, and tactile pattern accents while keeping the content practical and fast to scan.

## Content Model

Every operational page should include:

- Title.
- Description.
- Scope.
- Owner or ownership area.
- Current status.
- Acceptance criteria.
- Links to related evidence when available.

If a page documents an unfinished workflow, label the gap directly. Use `TBD` only when the owner and next action are also listed.

## Navigation Groups

Milestone 0 docs should remain grouped by working area:

- Dashboard: dashboard UX, state language, and future UI behavior.
- Operations: runbooks, observability, docs-site baseline, and operational handoff.
- Testing: verification plans, manual gates, and automation expectations.
- Release: governance, evidence, approval, and rollback.

Security and compliance content may live beside these groups, but this baseline does not redefine their ownership.

## Writing Rules

- Use direct, evidence-backed language.
- Do not invent metrics, customer claims, launch dates, testimonials, or partner names.
- Prefer short sections and actionable checklists.
- Keep terminology consistent with release states, severity levels, and dashboard state language.
- Mark placeholders as placeholders.
- Keep command slots generic until implementation owners add the actual runtime commands.

## Visual Rules

- Use warm cream surfaces and ink text as the default.
- Reserve terracotta for primary actions, warnings that require action, and active navigation.
- Use saffron and marine only as supporting accents.
- Keep docs tables readable at narrow widths or provide list alternatives.
- Avoid nested card layouts in docs examples.
- Use monospace for commands, event names, states, and metadata.

## Review Gates

Before a docs page is accepted:

- Frontmatter is present.
- The page has a clear scope.
- Cross-references use the same terms as related docs.
- No fabricated proof or placeholder business claims are present.
- Any manual process has an owner, trigger, and stop condition.
- Any UI guidance cites required responsive widths when relevant.

## Milestone 0 Acceptance

The docs site baseline is accepted when:

- Authoring rules are documented.
- Navigation groups match current ownership.
- Visual direction follows `design.md`.
- Release, testing, operations, and dashboard docs can cross-reference the same state and severity language.
