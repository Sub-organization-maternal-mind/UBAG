---
title: Artifacts
description: Screenshots, HAR, traces, recordings, DOM snapshots, and retention.
---

## Capture levels

- `minimal`: step logs and final failure screenshot.
- `debug`: DOM snapshots, console/network logs, screenshots.
- `full`: Playwright trace, HAR, optional WebM recording.

## Storage

Artifacts are stored through the `BlobStore` interface:

- edge: local content-addressed filesystem.
- small+: S3-compatible MinIO or Garage.

Current gateway-owned artifact routes are `/v1/jobs/{id}/artifacts` for listing and `/v1/jobs/{id}/artifacts/{key}` for upload, download, and delete. The in-memory artifact store is the default. `UBAG_ARTIFACT_STORE=minio` enables MinIO/S3 byte storage, while `migrations/postgres/0002_artifact_metadata.sql` provides Postgres metadata when the gateway store is also Postgres-backed.

## Metadata

Artifact metadata records URI, hash, MIME type, byte size, capture step, retention class, redaction status, job, attempt, tenant, target, and adapter.

## Privacy

Recordings and DOM snapshots can contain sensitive text. Standard mode allows operator-configured capture; HIPAA/GDPR modes restrict or disable sensitive capture by policy.
