---
title: Security Model
description: Authentication, authorization, audit, secrets, and safe automation controls.
---

## Default posture

Standard privacy and security mode is the default. HIPAA/GDPR modes are planned and enforce stricter logging, cache, retention, and export rules.

## Authentication

v0 starts with app-secret authentication. Short-lived RS256 app JWTs are available on the HTTP gateway: set `UBAG_APP_JWT_PUBLIC_KEY` (inline PEM, literal `\n` accepted) or `UBAG_APP_JWT_PUBLIC_KEY_FILE` (PEM file) and clients present `Authorization: Bearer <jwt>` carrying `tid` (tenant), `sub` (app id), `role` (e.g. `service`), and a required `exp`. Each downstream client app then gets its own isolated `(tenant, app)` scope for jobs, conversations, rate limits, and listings. Tokens with empty identity claims or no expiry are rejected. Device tokens, personal access tokens, and OIDC follow in v1; mTLS is a standard/enterprise path; gRPC remains app-secret-only.

## Authorization

RBAC roles are viewer, developer, operator, admin, and superadmin. ABAC checks include tenant, target, command type, quota, data classification, and app permissions.

## Secrets

Secrets are stored as argon2id hashes. Recoverable encrypted ciphertext is used only where the product truly needs it, such as webhook signing secret rotation.

## Audit

State-changing calls emit audit events. Manual browser takeover, secret rotation, webhook replay, role changes, adapter promotion, and policy changes are always audited.

## Safe browser automation

UBAG does not ship a CAPTCHA solver, does not scrape provider credentials, and does not bypass user ownership. Operators log in manually through short-lived live session access.
