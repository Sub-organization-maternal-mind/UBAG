---
title: Data And Storage
description: Database, cache, semantic cache, object storage, and profile-specific storage.
---

## Profiles

- edge: SQLite WAL, exact-hash cache, localfs blobs, and SQLite queue adapter are contracted with migrations/tests; current gateway runtime persistence is memory by default and Postgres/MinIO when configured.
- small: Postgres, MinIO, Dragonfly/Valkey, optional NATS.
- standard: Postgres with pgvector and partitioning, NATS JetStream, MinIO/Garage.
- enterprise: multi-region storage and residency controls.

## Object storage

The `BlobStore` interface supports put, get, head, delete, presign, list by prefix, and lifecycle metadata.

Current gateway artifact storage is exposed through `/v1/jobs/{id}/artifacts[/{key}]`. The default store is in-memory for local tests. Set `UBAG_ARTIFACT_STORE=minio` with `UBAG_MINIO_ENDPOINT`, `UBAG_MINIO_ACCESS_KEY`, `UBAG_MINIO_SECRET_KEY`, `UBAG_MINIO_BUCKET`, and `UBAG_MINIO_USE_SSL` to store artifact bytes in MinIO/S3-compatible object storage. When `UBAG_GATEWAY_STORE=postgres` is also active, metadata is stored in `artifact_metadata` from `migrations/postgres/0002_artifact_metadata.sql`; otherwise metadata is in memory.

## Cache safety

Semantic cache is tenant-partitioned and disabled for sensitive templates in HIPAA/GDPR modes. Cache writes require successful validated jobs.
