---
title: RBAC and ABAC
description: Authorization baseline for UBAG roles, attributes, and policy checks.
---

# RBAC and ABAC

Status: Milestone 0 docs-first baseline.

UBAG authorization uses RBAC for coarse capability groups and ABAC for contextual checks. RBAC alone is not sufficient because tenant boundaries, privacy mode, data class, resource ownership, and operational purpose all affect whether an action is allowed.

## Baseline Model

Every protected request should evaluate:

- Actor: authenticated user, service principal, or approved automation identity.
- Role: coarse permission group assigned to the actor.
- Tenant or workspace: the boundary the actor is operating inside.
- Resource: the object being read, created, updated, exported, or deleted.
- Action: the specific operation requested.
- Purpose: the business reason for access when the resource is sensitive.
- Privacy mode: `Standard` by default; future HIPAA/GDPR modes may add stricter gates.
- Environment: production, staging, preview, local, or support tooling.

## Roles

Milestone 0 role names are placeholders until product surfaces are finalized:

| Role | Intended scope | Notes |
| --- | --- | --- |
| Owner | Tenant administration and billing-sensitive settings | Requires stronger audit coverage. |
| Admin | Operational management within a tenant | Cannot bypass privacy mode or tenant boundaries. |
| Member | Normal authenticated product use | Least privilege by default. |
| Support | Time-bound support access | Requires explicit reason and audit event. |
| Service | Backend automation or integration | Must use scoped credentials and non-human audit identity. |

## ABAC Requirements

- Tenant and workspace boundaries are mandatory for user confidential data.
- Data class must be available to the authorization layer before sensitive reads or exports.
- Support access must include a reason, ticket/reference, actor, target tenant, time window, and resulting audit event.
- Administrative overrides must be rare, explicit, logged, and reviewed.
- Future HIPAA/GDPR modes must be able to add stricter policies without changing role names.

## Deny Conditions

Requests must be denied when:

- The actor is missing, disabled, or not strongly authenticated for the action.
- The role lacks the coarse capability.
- The tenant or resource boundary does not match.
- The action would expose a blocked or unapproved data class.
- The privacy mode requires controls that are not implemented.
- The request comes from browser automation without an approved safe-login context.

## Audit Hooks

Authorization should emit structured audit events for:

- `authz.allow` and `authz.deny` for privileged actions.
- Role assignment, removal, and privilege escalation.
- Support access start, use, and end.
- Policy changes and privacy-mode changes.
- Export, deletion, impersonation, and administrative override attempts.

