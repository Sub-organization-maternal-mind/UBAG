---
title: Audit Export And Merkle Chain
description: Tamper-evident audit log export with a Merkle/hash-chain integrity proof and chain_valid verification.
---

# Audit Export And Merkle Chain

UBAG keeps a tamper-evident audit log so an operator or auditor can prove that audit records were not altered or removed after the fact. This page covers blueprint section §11.6.

## Hash-chained audit log

Every audit entry is linked to the entry before it with a cryptographic hash. Each record carries the hash of the previous record, so the whole log forms a chain:

```
entry_n.prev_hash = hash(entry_{n-1})
entry_n.hash      = hash(entry_n.payload + entry_n.prev_hash)
```

Because each hash depends on the one before it, changing or deleting any earlier entry breaks every hash that follows. This makes silent tampering detectable.

## Export with integrity proof

The `POST /v1/audit/export` endpoint returns a bounded slice of the audit log together with an integrity proof. The export includes:

- the ordered audit entries for the requested range,
- the per-entry hashes that form the chain,
- a root/anchor value that commits to the exported set,
- a `chain_valid` boolean indicating whether the chain verified end-to-end on export.

A verifier can recompute the chain from the exported entries and confirm it matches the anchor. If any entry was modified, recomputation produces a different hash and `chain_valid` is `false`.

## chain_valid

`chain_valid` is the single, explicit signal an operator checks:

| Value | Meaning |
|---|---|
| `true` | The exported audit chain recomputed cleanly — no tampering detected in the range. |
| `false` | The chain did not verify — an entry was altered, reordered, or removed. Investigate. |

## What audit covers

Audit records capture safety- and ownership-relevant events, including:

- job creation, dispatch, and lifecycle transitions,
- manual-action alerts (CAPTCHA / login) and their acknowledge/resolve actions,
- session storage-state binding events (recorded as events, never as exported secrets),
- security-sensitive decisions and policy denials.

## Redaction

Audit export is tamper-evident, not a secret dump:

- credentials, cookies, tokens, and storage-state URIs are never written into audit payloads or exports,
- exports contain event metadata and hashes, not user-owned secrets,
- the integrity proof commits to the redacted records that are actually stored.
