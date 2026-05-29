---
title: Plugins
description: WASM plugin system and marketplace plan.
---

## Phase

Plugins are v2 scope. They are documented in Milestone 0 so the platform boundaries do not block future extensibility.

## Host model

UBAG uses WASM with explicit capabilities for network, filesystem, environment, secrets, time, and randomness.

## Plugin kinds

- Pre-job transform or reject.
- Post-job transform or forward.
- Adapter extension.
- Custom command type.
- Webhook transformer.
- Custom validator.

## Distribution

The marketplace is a signed manifest repository. Installation verifies signatures, requested capabilities, license metadata, and compatibility.
