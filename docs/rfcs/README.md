# UBAG RFCs

The RFC (Request for Comments) process is the mechanism for proposing significant changes to UBAG that require community discussion and design review.

## When to write an RFC

Write an RFC for changes that:
- Alter the public API surface
- Change the gateway/worker/sidecar protocol
- Introduce new deployment dependencies
- Change security or compliance posture
- Are architectural decisions that will be hard to reverse

You do NOT need an RFC for bug fixes, minor features, or documentation changes.

## Lifecycle

```
DRAFT → REVIEW → ACCEPTED/REJECTED → IMPLEMENTED
```

1. **DRAFT**: Copy `0000-template.md`, fill it out, open a PR
2. **REVIEW**: Maintainers and community comment for 2 weeks (minimum)
3. **ACCEPTED**: Maintainers merge the RFC PR; implementation can begin
4. **REJECTED**: RFC is closed with an explanation
5. **IMPLEMENTED**: RFC is marked implemented when the feature ships

## Index

| RFC | Title | Status |
|-----|-------|--------|
| [0000](0000-template.md) | Template | — |
| [0001](0001-strict-fidelity-v2.1.md) | Strict-Fidelity v2.1 | IMPLEMENTED |
