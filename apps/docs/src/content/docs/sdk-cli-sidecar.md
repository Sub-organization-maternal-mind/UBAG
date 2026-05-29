---
title: SDK, CLI, And Sidecar
description: Generated SDKs, CLI/TUI, and local connector plan.
---

## SDK strategy

SDKs are schema-driven from OpenAPI, Protobuf, and shared JSON Schema contracts with handwritten ergonomic layers. Generated contract manifests in the TypeScript, Python, and Go packages pin the OpenAPI/proto/schema hashes plus REST path and operation inventories; `pnpm check:sdk-freshness` blocks stale generated contract metadata.

## First SDK wave

TypeScript, Python, and Go ship first. Rust, .NET, and Java follow. Swift, Ruby, PHP, Dart, and Elixir complete the v2 set.

## Conformance

Every SDK must pass the same JSON-defined scenarios for success, errors, idempotency, retries, streaming, timeouts, Unicode, large payloads, malformed responses, sidecar discovery, and webhook verification. The current v0 executable baseline covers 23 REST scenarios across system, jobs, job events, artifacts, operator collections, webhook replay, and stable error envelopes. The fixture registry also names 12 non-executable coverage scenarios so language runners can enforce support consistently as each helper lands.

## CLI

The TypeScript CLI includes job submission, job status, event/operator/artifact/webhook commands, bounded SSE reads, diagnostics, and safe local adapter test commands. TUI mode follows the same command contract.

## Sidecar

The `@ubag/sidecar` package gives legacy apps a localhost API with loopback-only default binding, `/health`, `/v1/*` gateway proxying, and generated idempotency keys for mutating routes when callers omit them. OS keychain storage and offline queue semantics attach to the same package contract as deployment profiles harden.
