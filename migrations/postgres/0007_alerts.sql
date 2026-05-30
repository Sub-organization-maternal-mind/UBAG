-- Migration 0007: alerts
-- Native Postgres table for the gateway's human-in-the-loop manual-action
-- alerting subsystem. Apply after 0006_audit_sessions.sql.
--
-- When a worker reports that a job needs a manual human action (CAPTCHA,
-- manual login, or a verification challenge) the gateway raises an alert and
-- notifies a human operator (by email) so they can solve it in the live
-- browser session and let the flow resume. This is the ToS-safe design:
-- humans solve challenges, never the machine.
--
-- The attributes column holds canonical JSON serialized by the gateway. The
-- nullable *_at timestamps record lifecycle transitions (notified on dispatch,
-- acknowledged when an operator picks it up, resolved when the challenge is
-- solved). No credentials, cookies, or tokens are ever stored here.

CREATE TABLE IF NOT EXISTS gateway_alerts (
  alert_id    TEXT        NOT NULL,
  tenant_id   TEXT        NOT NULL,
  app_id      TEXT        NOT NULL DEFAULT '',
  job_id      TEXT        NOT NULL DEFAULT '',
  session_id  TEXT        NOT NULL DEFAULT '',
  target_id   TEXT        NOT NULL DEFAULT '',
  kind        TEXT        NOT NULL,
  message     TEXT        NOT NULL DEFAULT '',
  status      TEXT        NOT NULL DEFAULT 'open',
  created_at  TIMESTAMPTZ NOT NULL,
  notified_at TIMESTAMPTZ,
  acked_at    TIMESTAMPTZ,
  resolved_at TIMESTAMPTZ,
  attributes  TEXT        NOT NULL DEFAULT '{}',
  PRIMARY KEY (alert_id)
);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_tenant_created
  ON gateway_alerts (tenant_id, created_at);

CREATE INDEX IF NOT EXISTS idx_gateway_alerts_active
  ON gateway_alerts (tenant_id, job_id, kind, status);

INSERT INTO gateway_schema_migrations (version, name, checksum, applied_at)
VALUES ('0007', 'alerts', '', now())
ON CONFLICT (version) DO NOTHING;
