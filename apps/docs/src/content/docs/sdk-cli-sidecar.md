---
title: SDK, CLI, And Sidecar
description: Generated SDKs, CLI/TUI, and local connector plan.
---

## SDK strategy

SDKs are schema-driven from OpenAPI, Protobuf, and shared JSON Schema contracts with handwritten ergonomic layers. Generated contract manifests in the TypeScript and Go packages pin the OpenAPI path inventory, error catalog, and schema fingerprints; `pnpm check:sdk-freshness` blocks stale generated contract metadata.

## First SDK wave

TypeScript/JavaScript and Go are the only first-class SDK technologies for the active product scope. Prior Python, Rust, .NET, Java, Kotlin, Swift, Ruby, PHP, and Elixir SDK package trees were removed from the active workspace; Git history remains the archive if those ecosystems are revisited later.

## Conformance

Every supported SDK must pass the same JSON-defined scenarios for success, errors, idempotency, retries, streaming, timeouts, Unicode, large payloads, malformed responses, sidecar discovery, and webhook verification. The current executable baseline covers 41 REST scenarios across system, jobs, job events, artifacts, operator collections, webhook replay, browser/concurrency/alerts, audit export, SSO logout, and stable error envelopes. The fixture registry also names 272 non-executable coverage scenarios so the TypeScript and Go runners can enforce support consistently as each helper lands.

## CLI

The TypeScript CLI includes job submission, job status, event/operator/artifact/webhook commands, bounded SSE reads, diagnostics, and safe local adapter test commands. TUI mode follows the same command contract.

## Sidecar

The `@ubag/sidecar` package gives legacy apps a localhost API with loopback-only default binding, `/health`, `/v1/*` gateway proxying, and generated idempotency keys for mutating routes when callers omit them. OS keychain storage and offline queue semantics attach to the same package contract as deployment profiles harden.
