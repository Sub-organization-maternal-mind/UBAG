---
title: Adapter Contract
description: Target adapter manifest and runtime interface.
---

## Manifest

Each adapter declares:

- name and semver.
- target homepage.
- required login.
- supported command types.
- capabilities.
- selector strategies.
- artifact policy.
- synthetic test prompts.
- resource allow/block lists.

## Runtime hooks

Adapters implement health, login, conversation, submit, stream, completion, extraction, normalization, error hinting, and teardown hooks.

## Isolation

v1 uses trusted in-process Python adapters. Community or untrusted adapters move behind WASM or subprocess sandboxing in v2.
