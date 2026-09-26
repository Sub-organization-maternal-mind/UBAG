#!/bin/bash
# UBAG retry feeder: keeps a SHALLOW retry queue so no job waits past the
# reaper's 1h max lifetime (UBAG_JOB_MAX_LIFETIME_SECONDS, reaper enabled).
#
# Every cycle: if fewer than 3 jobs are in flight, POST retries for the 5
# OLDEST retryable-failed jobs (failed_retryable | timed_out) excluding
# chatgpt_web (operator login pending) and the impossible `gemini` row.
# Exits when the non-chatgpt failed set stays empty, or after MAX_MINUTES.

SECRET=$(grep '^UBAG_APP_SECRET=' "$(dirname "$0")/.env.local" | cut -d= -f2-)
GW='http://127.0.0.1:58080'
MAX_MINUTES=180
START=$SECONDS

while (( SECONDS - START < MAX_MINUTES * 60 )); do
  READOUT=$(curl -s --max-time 15 -H "Authorization: Bearer $SECRET" -H "Ubag-Api-Version: 2026-05-22" "$GW/v1/jobs?limit=100")
  PLAN=$(echo "$READOUT" | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>{try{
    const j=JSON.parse(d);
    if(!j.jobs) throw new Error('no jobs');
    const inflight=[],failed=[];
    for(const x of (j.jobs||[])){
      if(['queued','assigned','running','token_streaming','completing','created','scheduled'].includes(x.status)) inflight.push(x.job_id);
      if(['failed_retryable','timed_out'].includes(x.status) && x.job_id!=='job_000000000247' && ['duckai_web','gemini_web','deepseek_web'].includes(x.target)) failed.push(x.job_id);
    }
    failed.reverse(); // oldest first
    console.log(JSON.stringify({inflight:inflight.length,failed}));
  }catch(e){console.log(JSON.stringify({error:String(e)}))}})")
  # A transient API failure must SKIP the cycle, never read as "nothing left"
  # — otherwise the feeder exits early and the drain stalls.
  if echo "$PLAN" | grep -q '"error"'; then
    echo "[$(date +%H:%M)] api read failed - skipping cycle"
    sleep 60
    continue
  fi
  INFLIGHT=$(echo "$PLAN" | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>console.log(JSON.parse(d).inflight))")
  FAILED=$(echo "$PLAN" | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>console.log(JSON.parse(d).failed.join(' ')))")
  echo "[$(date +%H:%M)] inflight=$INFLIGHT failed_nonchatgpt=$(echo $FAILED | wc -w)"
  if [ -z "$FAILED" ]; then
    echo "[$(date +%H:%M)] nothing left to retry - done."
    exit 0
  fi
  if [ "$INFLIGHT" -lt 3 ]; then
    N=0
    for ID in $FAILED; do
      [ $N -ge 5 ] && break
      R=$(curl -s --max-time 60 -X POST -H "Authorization: Bearer $SECRET" -H "Content-Type: application/json" -H "Ubag-Api-Version: 2026-05-22" -H "Idempotency-Key: feeder-$ID-$(date +%s)" -d '{}' "$GW/v1/jobs/$ID/retry" | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>{try{const j=JSON.parse(d);console.log(j.job_id||'ERR')}catch(e){console.log('ERR')}})")
      echo "[$(date +%H:%M)] retried $ID -> $R"
      N=$((N+1))
      sleep 1
    done
  fi
  sleep 60
done
echo "[$(date +%H:%M)] feeder time limit reached."
