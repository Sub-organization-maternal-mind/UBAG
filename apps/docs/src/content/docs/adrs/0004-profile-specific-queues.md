---
title: "ADR 0004: Profile-Specific Queues"
description: Edge needs a SQLite queue adapter instead of River.
---

## Status

Accepted.

## Decision

UBAG defines a profile-neutral Queue interface with SQLite, Postgres/River-compatible, and NATS JetStream implementations.

## Consequences

The edge profile can remain SQLite-only while standard deployments use NATS JetStream.
