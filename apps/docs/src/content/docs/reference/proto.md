---
title: Proto Reference
description: Protobuf definitions for UBAG SSE stream contracts and internal gRPC services.
---

The UBAG gateway uses protobuf-defined message types for its SSE stream payloads and internal gRPC interfaces. The canonical definitions live in `packages/proto/`.

## Directory layout

| Path | Description |
|------|-------------|
| `packages/proto/ubag/v1/job.proto` | Job create/update/cancel messages |
| `packages/proto/ubag/v1/stream.proto` | SSE envelope types (JobEvent, LogLine, ArtifactRef) |
| `packages/proto/ubag/v1/worker.proto` | Gateway↔Worker internal gRPC contract |
| `packages/proto/ubag/v1/audit.proto` | Audit log entry shape |

## Core message types

### JobEvent (stream.proto)

Emitted on the `/v1/jobs/{id}/events` SSE stream:

```proto
message JobEvent {
  string job_id = 1;
  JobStatus status = 2;
  google.protobuf.Timestamp occurred_at = 3;
  oneof payload {
    LogLine log = 4;
    ArtifactRef artifact = 5;
    JobError error = 6;
    BrowserEvent browser = 7;
  }
}
```

### LogLine

```proto
message LogLine {
  string level  = 1;   // "info" | "warn" | "error"
  string source = 2;   // "worker" | "adapter" | "browser"
  string body   = 3;
}
```

### ArtifactRef

```proto
message ArtifactRef {
  string artifact_id = 1;
  string kind        = 2;   // "screenshot" | "har" | "trace" | "dom"
  string url         = 3;   // pre-signed URL (15 min TTL)
  int64  size_bytes  = 4;
}
```

### JobStatus enum

```proto
enum JobStatus {
  JOB_STATUS_UNSPECIFIED = 0;
  JOB_STATUS_QUEUED      = 1;
  JOB_STATUS_RUNNING     = 2;
  JOB_STATUS_DONE        = 3;
  JOB_STATUS_FAILED      = 4;
  JOB_STATUS_CANCELLED   = 5;
  JOB_STATUS_TIMED_OUT   = 6;
}
```

## Generating client stubs

```bash
# TypeScript (requires @bufbuild/protoc-gen-es)
buf generate packages/proto

# Go
buf generate packages/proto --template buf.gen.go.yaml

# Python
buf generate packages/proto --template buf.gen.python.yaml
```

## SSE encoding

The gateway JSON-encodes JobEvent messages over SSE. The field names follow
the proto JSON mapping (camelCase). Example SSE frame:

```
event: job_event
data: {"jobId":"abc-123","status":"JOB_STATUS_RUNNING","occurredAt":"2026-05-22T10:00:00Z","log":{"level":"info","source":"adapter","body":"Page loaded"}}
```

## Worker gRPC contract

The gateway exposes an internal gRPC service that workers connect to over mTLS:

```proto
service WorkerGateway {
  rpc ClaimJob     (ClaimRequest)  returns (JobSpec);
  rpc ReportEvent  (JobEvent)      returns (google.protobuf.Empty);
  rpc UploadArtifact (stream ArtifactChunk) returns (ArtifactRef);
  rpc CompleteJob  (CompleteRequest) returns (google.protobuf.Empty);
}
```

Workers authenticate with a client certificate issued per deployment. The
certificate CN must match the `worker_id` in `ClaimRequest`.

## Buf configuration

The project uses [Buf](https://buf.build/) for linting and breaking-change detection:

```bash
# Lint proto files
buf lint packages/proto

# Check for breaking changes against the BSR
buf breaking packages/proto --against 'buf.build/ubag/ubag'
```

CI runs both checks on every PR that touches `packages/proto/`.

## Helper Node contract (`ubag.helper.v1`)

`packages/proto/proto/ubag/helper/v1/helper.proto` defines `HelperService`, the primary-to-Helper-Node execution contract (a separate package from the public `ubag.v1`). The primary is the gRPC client and dials the helper over WireGuard; the helper is the mTLS gRPC server. RPCs: `Handshake`, `ReportCapacity` (15 s), `RunAttempt` (server stream), `RenewAttempt`, `CancelAttempt`, `InspectAttempt`, `Drain`, `StageManifest`. Every mutating RPC carries a `Fence` (job, attempt, node, lease generation, lease expiry, input fingerprint, workload version); the event key is `attempt_id:sequence`, and `PROMPT_SUBMITTED` is the submission boundary. Staging uploads use the helper's mTLS HTTPS listener, outside `/v1`. Contract only: no gateway or helper implements it yet, and it is inert.

### Helper voice (`HelperVoiceService`)

`helper_voice.proto` adds `HelperVoiceService` to the same package (additive; `HelperService` is unchanged): `OfferVoice`, `ReconnectVoice`, `ControlVoice` (mute, unmute, interrupt, terminate), `RenewVoice` and the server stream `StreamVoiceEvents`. Media terminates on the helper; the primary keeps public signaling, so the OpenAPI connect response is unchanged. Every request carries a `VoiceFence` (session, tenant, attempt, node, lease generation, expiry). The helper never holds the app secret, the relay secret or the TURN shared secret: the primary derives per-attempt relay and media keys and mints time-limited TURN credentials (`VoiceCredentials`), and the relay hello binds node and generation (relay protocol: optional `node_id` + `generation`). Gated by `UBAG_HELPER_VOICE` (default off, requires the helper flag ladder).

The helper side is served by `ubag-helper` built with `-tags helpervoice` and `UBAG_HELPER_VOICE=1` (the default binary refuses to start with it on). It terminates the WebRTC media itself (`voice.MediaHub` on a loopback relay, one exclusive audio environment per call, a bounded UDP range, NAT 1:1 and TURN), activates the provider's voice UI through its own worker against its own loopback browser, and ends a call by itself when the lease is not renewed. `ControlVoice` interrupt answers `UNIMPLEMENTED`: no provider-side barge-in primitive exists yet. The primary dials it through `voice.RemoteNegotiator` (P5.11, same flag and the whole helper ladder): the offer is routed by the session's node, every call carries the `VoiceFence` of the lease the voice store holds, a per-call supervisor renews the store's media lease first and the node's lease second, and the node's events become fenced primary writes (`ENDED` releases the terminating hold). The connect response is unchanged. See ADR-0018.
