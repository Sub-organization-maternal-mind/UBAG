---
title: API Protocols
description: REST, WebSocket, SSE, gRPC, MessagePack, MQTT, and integration patterns.
---

## Protocol phases

| Protocol | Phase | Purpose |
| --- | --- | --- |
| REST/JSON | v0 | Default client API. |
| SSE | v0 | One-way job events for browsers and simple clients. |
| WebSocket | v0-v1 | v0 ships a guarded upgrade, heartbeat, and event-stream baseline; v1 adds richer bidirectional/dashboard semantics. |
| gRPC/gRPC-Web | v1 | Strongly typed service clients; not served by the current gateway until the protobuf transport adapter lands. |
| MessagePack | v1-v2 | Smaller payloads for selected clients. |
| MQTT 5 | v2 plugin | IoT and intermittent edge clients. |

## Versioning

Routes use `/v1` for route stability. Behavior versioning uses `Ubag-Api-Version`; request bodies can include `api_version` but must match the header when both are present.

## Integration patterns

- Direct SDK/REST.
- Streaming over SSE, WebSocket, or gRPC server streams.
- Local sidecar bridge.
- CLI/subprocess bridge.
- Signed webhook callbacks.
- Workflow chains using the current single-step runtime boundary first, then fuller saga execution in v1 hardening.
