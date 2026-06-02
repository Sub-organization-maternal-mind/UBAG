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
