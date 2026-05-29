---
title: "ADR 0002: Schema-Driven Contracts"
description: OpenAPI, Protobuf, and JSON Schema drive public contracts and SDK conformance.
---

## Status

Accepted.

## Decision

UBAG keeps REST contracts in OpenAPI 3.1, gRPC contracts in Protobuf, and command shapes in JSON Schema. Local gates enforce contract parity and generated SDK contract-manifest freshness.

## Consequences

SDK contract metadata can be generated consistently while still allowing idiomatic handwritten client layers.
