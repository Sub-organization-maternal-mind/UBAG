-- Migration 0005: alerts
-- SQLite parity of migrations/postgres/0007_alerts.sql for the gateway's
-- human-in-the-loop manual-action alerting subsystem. Timestamps are RFC3339
-- UTC strings (empty string when unset). Apply after 0004_browser_topology.sql.
--
-- When a worker reports that a job needs a manual human action (CAPTCHA,
-- manual login, or a verification challenge) the gateway raises an alert and
-- notifies a human operator (by email) so they can solve it in the live
-- browser session and let the flow resume. No credentials, cookies, or tokens
-- are ever stored here.

CREATE TABLE IF NOT EXISTS gateway_alerts (
  alert_id    TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  app_id      TEXT NOT NULL DEFAULT '',
  job_id      TEXT NOT NULL DEFAULT '',
  session_id  TEXT NOT NULL DEFAULT '',
  target_id   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,
  message     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  notified_at TEXT NOT NULL DEFAULT '',
  acked_at    TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT '',
  attributes  TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_tenant_created
  ON gateway_alerts (tenant_id, created_at);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_active
  ON gateway_alerts (tenant_id, job_id, kind, status);

INSERT OR IGNORE INTO gateway_schema_migrations (version, name, checksum)
VALUES ('0005', 'alerts', 'manual-v0-sqlite-alerts');
