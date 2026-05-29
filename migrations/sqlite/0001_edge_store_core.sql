-- UBAG edge-store baseline for SQLite-compatible runtimes.
-- Timestamps are RFC3339 UTC strings. JSON values are stored as TEXT.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS edge_schema_migrations (
  version TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS edge_idempotency_keys (
  scope TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'locked'
    CHECK (status IN ('locked', 'succeeded', 'failed', 'expired')),
  locked_until TEXT,
  response_json TEXT CHECK (response_json IS NULL OR json_valid(response_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  expires_at TEXT,
  PRIMARY KEY (scope, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_edge_idempotency_resource
  ON edge_idempotency_keys (resource_type, resource_id);

CREATE INDEX IF NOT EXISTS idx_edge_idempotency_expiry
  ON edge_idempotency_keys (expires_at)
  WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS edge_blob_objects (
  object_key TEXT PRIMARY KEY,
  bucket TEXT NOT NULL DEFAULT 'default',
  content_type TEXT,
  byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
  sha256 TEXT,
  metadata_json TEXT CHECK (metadata_json IS NULL OR json_valid(metadata_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  expires_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_edge_blob_objects_bucket_created
  ON edge_blob_objects (bucket, created_at);

CREATE INDEX IF NOT EXISTS idx_edge_blob_objects_expiry
  ON edge_blob_objects (expires_at)
  WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS edge_outbox_events (
  id TEXT PRIMARY KEY,
  topic TEXT NOT NULL,
  event_name TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'dispatched', 'failed', 'cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_error_json TEXT CHECK (last_error_json IS NULL OR json_valid(last_error_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  dispatched_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_edge_outbox_pending
  ON edge_outbox_events (status, available_at, created_at);

INSERT OR IGNORE INTO edge_schema_migrations (version, name, checksum)
VALUES ('0001', 'edge_store_core', 'manual-v0');
