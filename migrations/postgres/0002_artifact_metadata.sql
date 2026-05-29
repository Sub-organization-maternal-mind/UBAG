-- Migration 0002: artifact_metadata
-- Stores gateway-owned artifact metadata for job artifacts stored in MinIO.
-- Apply after 0001_gateway_stores.sql.

CREATE TABLE IF NOT EXISTS artifact_metadata (
    job_id          TEXT        NOT NULL,
    artifact_key    TEXT        NOT NULL,
    bucket          TEXT        NOT NULL DEFAULT 'ubag-artifacts',
    object_key      TEXT,
    content_type    TEXT        NOT NULL DEFAULT 'application/octet-stream',
    size_bytes      BIGINT      NOT NULL DEFAULT 0,
    checksum        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, artifact_key)
);

CREATE INDEX IF NOT EXISTS artifact_metadata_job_created_at_idx
    ON artifact_metadata (job_id, created_at DESC, artifact_key ASC);

INSERT INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0002', 'artifact_metadata', 'manual-v0-postgres-artifacts')
ON CONFLICT (version) DO NOTHING;
