# EXECUTOR TASK — vps2 resource raise + distribution half (2026-09-27)

Owner directive 2026-09-27: browser containers at **2.0 CPU / 4GB** on BOTH
boxes; record each box's total capacity; run the vps2 half of the workload
distribution test. Primary (185.252.233.186) is already done.

---8<--- paste from here ---

ROLE: You are the vps2 executor session (on-box at 213.163.201.37,
/opt/docker/ubag, compose file docker-compose.vps2.yml, env at
deploy/vps2/env.local).

CONTEXT (verified 2026-09-27 — trust, re-verify only what the TASK touches):
- The primary's browser container was raised to cpus=2.0 / mem_limit=4096m
  and recreated healthy (commit ebe8d3a+ contains the same compose change).
- The live-browser bridge was rebuilt into the primary image (client registry
  + kick/visibility protocol); vps2 gets it with the same rebuild below.
- The primary ran an incident cleanup: orphaned spool leases recovered at
  gateway startup (gateway code now does this automatically — vps2 gets it
  when it syncs the new commit).

HARD RULES (violating these fails the task):
1. Never touch UBAG's stored credentials, provider sessions, or the browser
   profile volume. Recreating the browser container keeps logins (named
   volume); do NOT delete volumes.
2. Read-only provider interaction only. If a provider shows a login wall,
   report it — never automate logins.
3. Report exact numbers (nproc, free -m) — do not guess the box's capacity.

RUNBOOK:
A. Record capacity (paste into the report):
   nproc; free -m; docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}'
B. Sync the repo checkout to the latest main (same tarball/git path as the
   cf8de88 sync — the compose + bridge changes ride with it). Verify:
   grep -A1 'container_name: ubag-vps2-browser' docker-compose.vps2.yml
   shows cpus "2.0" and mem_limit 4096m.
C. If the box has < 8GB RAM or < 4 cores, STOP and report before applying —
   the limits must fit the box.
D. Apply: cd /opt/docker/ubag && docker compose --env-file deploy/vps2/env.local \
   -f docker-compose.vps2.yml up -d --build browser
   Then: docker inspect ubag-vps2-browser --format 'cpus={{.HostConfig.NanoCpus}} mem={{.HostConfig.Memory}}'
   and docker ps --filter name=ubag-vps2-browser --format '{{.Status}}'
E. Distribution half (vps2 side): submit ONE mock job to the LOCAL gateway
   (127.0.0.1:58080 on this box, deploy-smoke credential):
   curl -s -X POST http://127.0.0.1:58080/v1/jobs -H "Authorization: Bearer $SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" -H "Content-Type: application/json" \
     -H "Idempotency-Key: dist-test-vps2-1" \
     -d '{"target":"mock","command_type":"mock.complete","client":{"app_id":"dist-test"},"input":{"prompt":"dist-test-vps2-1"}}'
   Wait ~15s, then confirm it COMPLETED ON THIS BOX (ls /opt/docker/ubag/spool/done
   contains the new job envelope; the primary's spool does not).

VERIFY (always):
- docker inspect browser limits = 2147483648 nano-cpus? No: cpus=2.0 => NanoCpus 2000000000, mem=4294967296.
- Browser container healthy (healthcheck covers bridge 58090 + CDP 58091).
- The mock job completed on THIS box's spool (distribution test evidence).
- Report the box's nproc/free -m numbers.

REPORT FORMAT (final message, nothing else):
TASK-STATUS: DONE | PARTIAL | FAILED
CAPACITY: nproc=, ram_mb=
LIMITS APPLIED: cpus=, mem=
DIST-TEST: job_id=, completed_on=vps2, primary_spool_untouched=yes/no
ANOMALIES: ...
---8<--- paste ends ---
