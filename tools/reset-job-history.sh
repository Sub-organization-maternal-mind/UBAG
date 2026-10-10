#!/usr/bin/env bash
# Production maintenance: explicit cutoff, restricted recovery snapshot, no sequence reset.
set -euo pipefail
umask 077
[[ ${1:-} == --execute ]] || { echo 'Usage: reset-job-history.sh --execute (all UBAG tenants)'; exit 2; }
ROOT=/opt/docker/ubag
[[ -d "$ROOT" ]] || exit 2
for c in ubag-nginx-dashboard ubag-vps-gateway-1 ubag-vps-chat-reaper; do
  [[ $(docker inspect -f '{{.State.Running}}' "$c") == true ]] || { echo "Refusing: $c is not running"; exit 2; }
done
q() { docker exec -i platform-postgres sh -c 'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d ubag "$@"' sh "$@"; }
active=$(q -Atc "select count(*) from gateway_jobs where status in ('created','scheduled','queued','assigned','running')")
[[ $active == 0 ]] || { echo 'Refusing: active jobs exist'; exit 2; }
active=$(q -Atc "select count(*) from gateway_voice_sessions where status in ('queued','activating','connected','active','connecting')")
[[ $active == 0 ]] || { echo 'Refusing: active voice sessions exist'; exit 2; }
cutoff=$(q -Atc "select to_char(clock_timestamp() at time zone 'UTC','YYYY-MM-DD HH24:MI:SS.US')")
snapshot="$ROOT/maintenance/job-reset-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$snapshot"
printf '%s\n' "$cutoff" > "$snapshot/cutoff.txt"
q -Atc "select id from gateway_jobs where created_at <= '$cutoff UTC' order by id" > "$snapshot/job-ids.txt"
export UBAG_RESET_SNAPSHOT="$snapshot"
python3 - <<'PY'
import json,os,re
from pathlib import Path
snapshot=Path(os.environ['UBAG_RESET_SNAPSHOT'])
ids=set(snapshot.joinpath('job-ids.txt').read_text().splitlines())
assert ids and all(re.fullmatch(r'job_[0-9]{12}',s) for s in ids)
roots=[Path('/var/lib/docker/volumes/ubag-vps_artifact_data/_data'),Path('/var/lib/docker/volumes/ubag-vps_executor_spool/_data')]
files=[]
for root in roots:
 assert root.is_dir() and not root.is_symlink()
 for p in root.rglob('*'):
  rel=p.relative_to(root)
  owned=rel.parts[0] in ids if root==roots[0] else re.split(r'[.]',p.name)[0] in ids
  if not owned: continue
  assert not p.is_symlink() and p.resolve().is_relative_to(root.resolve())
  if p.is_file(): files.append(str(p))
snapshot.joinpath('files.json').write_text(json.dumps(files))
snapshot.joinpath('files.nul').write_bytes(b''.join(p.encode()+b'\0' for p in files))
print(json.dumps({'selected_jobs':len(ids),'selected_files':len(files),'snapshot':str(snapshot)}))
PY
resume() { docker start ubag-vps-gateway-1 ubag-vps-chat-reaper ubag-nginx-dashboard >/dev/null; }
trap resume EXIT
docker stop -t 30 ubag-nginx-dashboard ubag-vps-chat-reaper ubag-vps-gateway-1 >/dev/null
# Recheck after draining: no admitted job may be abandoned by maintenance.
[[ $(q -Atc "select count(*) from gateway_jobs where status in ('created','scheduled','queued','assigned','running')") == 0 ]] || exit 2
docker exec platform-postgres sh -c 'pg_dump -Fc -U "$POSTGRES_USER" -d ubag' > "$snapshot/ubag.dump"
docker exec -i platform-postgres sh -c 'pg_restore -l' < "$snapshot/ubag.dump" > "$snapshot/restore-list.txt"
tar --null -T "$snapshot/files.nul" -czf "$snapshot/job-files.tar.gz" 2> "$snapshot/tar.log"
q <<SQL
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
CREATE TEMP TABLE reset_ids AS SELECT id FROM gateway_jobs WHERE created_at <= '$cutoff UTC';
CREATE UNIQUE INDEX ON reset_ids(id);
DELETE FROM gateway_webhook_attempts WHERE delivery_id IN (SELECT id FROM gateway_webhook_deliveries WHERE job_id IN (SELECT id FROM reset_ids));
DELETE FROM gateway_webhook_deliveries WHERE job_id IN (SELECT id FROM reset_ids);
DELETE FROM artifact_metadata WHERE job_id IN (SELECT id FROM reset_ids);
DELETE FROM gateway_idempotency_records WHERE resource_id IN (SELECT id FROM reset_ids);
DELETE FROM gateway_alerts WHERE job_id IN (SELECT id FROM reset_ids);
DELETE FROM gateway_admission_token_lanes WHERE token_id IN (SELECT token_id FROM gateway_admission_tokens WHERE job_id IN (SELECT id FROM reset_ids));
DELETE FROM gateway_admission_tokens WHERE job_id IN (SELECT id FROM reset_ids);
UPDATE gateway_conversations SET last_job_id='' WHERE last_job_id IN (SELECT id FROM reset_ids);
UPDATE gateway_browser_sessions SET current_job_id=NULL WHERE current_job_id IN (SELECT id FROM reset_ids);
UPDATE gateway_browser_tabs SET current_job_id=NULL WHERE current_job_id IN (SELECT id FROM reset_ids);
UPDATE gateway_voice_sessions SET job_id='' WHERE job_id IN (SELECT id FROM reset_ids);
CREATE FUNCTION pg_temp.clear_job_refs(v jsonb) RETURNS jsonb LANGUAGE plpgsql AS \$fn\$
DECLARE out_value jsonb;
BEGIN
 CASE jsonb_typeof(v)
 WHEN 'string' THEN
  IF EXISTS(SELECT 1 FROM reset_ids WHERE id=v#>>'{}') THEN RETURN to_jsonb(''::text); END IF;
 WHEN 'array' THEN
  SELECT coalesce(jsonb_agg(pg_temp.clear_job_refs(value)),'[]'::jsonb) INTO out_value FROM jsonb_array_elements(v);
  RETURN out_value;
 WHEN 'object' THEN
  SELECT coalesce(jsonb_object_agg(key,pg_temp.clear_job_refs(value)),'{}'::jsonb) INTO out_value FROM jsonb_each(v);
  RETURN out_value;
 ELSE NULL;
 END CASE;
 RETURN v;
END \$fn\$;
UPDATE gateway_workflow_runs SET steps_json=pg_temp.clear_job_refs(steps_json) WHERE steps_json IS NOT NULL;
-- Cached job responses must not resurrect removed history.
DELETE FROM gateway_response_cache WHERE tenant_id IN (SELECT DISTINCT tenant_id FROM gateway_jobs WHERE id IN (SELECT id FROM reset_ids));
DELETE FROM gateway_jobs WHERE id IN (SELECT id FROM reset_ids);
COMMIT;
SQL
touch "$snapshot/database-deleted"
python3 - <<'PY'
import json,os
from pathlib import Path
s=Path(os.environ['UBAG_RESET_SNAPSHOT'])
files=json.loads(s.joinpath('files.json').read_text())
roots=[Path('/var/lib/docker/volumes/ubag-vps_artifact_data/_data'),Path('/var/lib/docker/volumes/ubag-vps_executor_spool/_data')]
for name in files:
 p=Path(name)
 assert any(p.resolve().is_relative_to(r.resolve()) for r in roots) and not p.is_symlink()
 p.unlink(missing_ok=True)
assert not any(Path(p).exists() for p in files)
s.joinpath('files-deleted').touch()
print('Selected files removed; recovery snapshot retained mode 0700.')
PY
q -Atc "select 'remaining_selected_jobs='||count(*) from gateway_jobs where created_at <= '$cutoff UTC'"
q -Atc "select 'orphan_events='||count(*) from gateway_job_events e where not exists(select 1 from gateway_jobs j where j.id=e.job_id)"
q -Atc "select 'next_id_sequence='||last_value from gateway_job_id_seq"
resume
trap - EXIT
echo "Reset complete. Recovery: $snapshot"
