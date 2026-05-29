---
title: Documentation System
description: How UBAG keeps planning, product, architecture, and operations documentation reviewable.
---

## Purpose

Milestone 0 makes documentation a product artifact. The docs site is not marketing copy; it remains the source of implementation truth throughout staged platform delivery.

## Required artifacts

- `PRD.md`: product requirements, goals, non-goals, phases, risks.
- `PROGRESS.md`: live progress ledger and blueprint feature map.
- Starlight docs: navigable topic docs for every feature area.
- ADRs: locked architecture and delivery decisions.
- Coverage check: a script that fails when required docs are missing.

## Update rules

- Every feature implementation must update the relevant docs before or with code.
- Every public contract change needs an ADR or a contract doc update.
- `PROGRESS.md` must track completed work, pending work, blockers, and verification evidence.
- No fabricated metrics, customer claims, or testimonials are allowed.
