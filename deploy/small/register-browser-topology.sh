#!/bin/sh
set -eu

tenant_id="${UBAG_TOPOLOGY_TENANT_ID:-tenant_edge}"
instance_id="${UBAG_TOPOLOGY_INSTANCE_ID:-br_prod_browser_viewer}"
worker_id="${UBAG_TOPOLOGY_WORKER_ID:-browser-viewer}"
browser_ip="${UBAG_BROWSER_PRIVATE_IP:-172.31.0.5}"
remote_endpoint="${UBAG_REMOTE_BROWSER_ENDPOINT:-http://${browser_ip}:9223}"

# Credentials: the platform issues ONE DSN per project and that DSN is the
# authoritative credential (the standalone POSTGRES_* keys are a fallback for
# a self-hosted database and drift out of date when the password is rotated).
# Reading the DSN first is what makes this script work against the shared
# platform Postgres instead of failing with a password error every 60 s.
postgres_host="${POSTGRES_HOST:-postgres}"
postgres_user="${POSTGRES_USER:-ubag}"
postgres_db="${POSTGRES_DB:-ubag}"
postgres_pass="${POSTGRES_PASSWORD:-}"
if [ -n "${UBAG_POSTGRES_DSN:-}" ]; then
  dsn_rest="${UBAG_POSTGRES_DSN#*://}"      # user[:pass]@host[:port]/db?params
  dsn_userinfo="${dsn_rest%%@*}"
  dsn_hostpart="${dsn_rest#*@}"
  if [ "$dsn_userinfo" != "$dsn_rest" ]; then
    dsn_user="${dsn_userinfo%%:*}"
    case "$dsn_userinfo" in
      *:*) dsn_pass="${dsn_userinfo#*:}" ;;
    esac
    dsn_host="${dsn_hostpart%%/*}"
    dsn_host="${dsn_host%%:*}"   # strip the port: psql takes it separately
    dsn_db="${dsn_hostpart#*/}"
    dsn_db="${dsn_db%%\?*}"
    [ -n "$dsn_user" ] && postgres_user="$dsn_user"
    [ -n "$dsn_host" ] && postgres_host="$dsn_host"
    [ -n "$dsn_db" ] && postgres_db="$dsn_db"
    [ -n "$dsn_pass" ] && postgres_pass="$dsn_pass"
  fi
fi

PGPASSWORD="$postgres_pass" psql -v ON_ERROR_STOP=1 \
  -v tenant_id="${tenant_id}" \
  -v instance_id="${instance_id}" \
  -v worker_id="${worker_id}" \
  -v remote_endpoint="${remote_endpoint}" \
  -h "${postgres_host}" \
  -U "${postgres_user}" \
  -d "${postgres_db}" <<SQL
INSERT INTO gateway_browser_instances (
  instance_id, worker_id, tenant_id, engine, remote_endpoint, state,
  context_count, tab_count, created_at
) VALUES (
  :'instance_id', :'worker_id', :'tenant_id', 'chromium', :'remote_endpoint',
  'ready', 4, 4, now()
)
ON CONFLICT (instance_id) DO UPDATE SET
  worker_id = EXCLUDED.worker_id,
  tenant_id = EXCLUDED.tenant_id,
  engine = EXCLUDED.engine,
  remote_endpoint = EXCLUDED.remote_endpoint,
  state = EXCLUDED.state,
  context_count = EXCLUDED.context_count,
  tab_count = EXCLUDED.tab_count;

-- login_state is a GENESIS-ONLY seed here, never re-asserted.
--
-- This script registers the browser->context->tab STRUCTURE and (via the sync
-- loop) runs every ~60s. It must NOT own login_state: the live worker's real
-- detect_login_state result is the source of truth, projected onto these exact
-- rows by the gateway (session.authenticated -> 'authenticated',
-- session.manual_action_required -> 'login_required', keyed by tenant+target_id).
-- login_state is therefore intentionally ABSENT from the DO UPDATE SET below, so
-- a re-run can never clobber the worker-written value back to a fiction — the bug
-- this fixes was the cron re-stamping 'authenticated' every 60s, which masked a
-- dead session for as long as it stayed dead.
--
-- The INSERT seed only applies the FIRST time a context is registered (a fresh
-- database). The curated primaries seed 'authenticated' rather than 'unknown' on
-- purpose: RadioPad disables any provider whose login_state != 'authenticated',
-- and a disabled provider receives no traffic, so the worker would never run a
-- job against it and could never PROVE it is logged in — a cold-start deadlock.
-- The optimistic seed lets the first real job correct the value; after that the
-- worker owns it permanently. chatgpt_web is not a curated primary, so it seeds
-- the honest 'unknown'.
INSERT INTO gateway_provider_contexts (
  context_id, instance_id, tenant_id, target_id, identity_ref, login_state,
  conversation_model, max_tabs, created_at, last_health_at
) VALUES
  ('ctx_prod_chatgpt', :'instance_id', :'tenant_id', 'chatgpt_web', 'production-manual-openai', 'unknown', 'spa-singleton', 2, now(), now()),
  ('ctx_prod_gemini', :'instance_id', :'tenant_id', 'gemini_web', 'production-manual-google', 'authenticated', 'spa-singleton', 2, now(), now()),
  ('ctx_prod_deepseek', :'instance_id', :'tenant_id', 'deepseek_web', 'production-manual-deepseek', 'authenticated', 'spa-singleton', 2, now(), now()),
  ('ctx_prod_duckai', :'instance_id', :'tenant_id', 'duckai_web', 'production-manual-duckai', 'unknown', 'spa-singleton', 2, now(), now())
ON CONFLICT (context_id) DO UPDATE SET
  instance_id = EXCLUDED.instance_id,
  tenant_id = EXCLUDED.tenant_id,
  target_id = EXCLUDED.target_id,
  identity_ref = EXCLUDED.identity_ref,
  conversation_model = EXCLUDED.conversation_model,
  max_tabs = EXCLUDED.max_tabs,
  last_health_at = EXCLUDED.last_health_at;

INSERT INTO gateway_browser_tabs (
  tab_id, context_id, state, conversation_id, jobs_completed, last_health_at, created_at
) VALUES
  ('tab_prod_chatgpt', 'ctx_prod_chatgpt', 'warming', 'https://chatgpt.com/', 0, now(), now()),
  ('tab_prod_gemini', 'ctx_prod_gemini', 'ready', 'https://gemini.google.com/app', 0, now(), now()),
  ('tab_prod_deepseek', 'ctx_prod_deepseek', 'ready', 'https://chat.deepseek.com/', 0, now(), now()),
  ('tab_prod_duckai', 'ctx_prod_duckai', 'warming', 'https://duck.ai/', 0, now(), now())
ON CONFLICT (tab_id) DO UPDATE SET
  context_id = EXCLUDED.context_id,
  state = EXCLUDED.state,
  conversation_id = EXCLUDED.conversation_id,
  last_health_at = EXCLUDED.last_health_at;
SQL

echo "browser topology registered for tenant ${tenant_id}"
