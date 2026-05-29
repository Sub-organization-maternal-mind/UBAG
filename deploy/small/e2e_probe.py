#!/usr/bin/env python3
"""UBAG gateway role-based E2E probe.

Runs INSIDE the gateway container (python3 present) against the local
gateway on 127.0.0.1:8080, bypassing the caddy edge so internal routes
(/v1/ready, /v1/metrics) are also reachable.

Usage:
    UBAG_APP_SECRET=... ROLE_LABEL=service python3 e2e_probe.py
"""
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("UBAG_BASE", "http://127.0.0.1:8080")
SECRET = os.environ.get("UBAG_APP_SECRET", "")
ROLE = os.environ.get("ROLE_LABEL", "(unset)")
API = os.environ.get("UBAG_API_VERSION", "2026-05-22")

results = []


def call(method, path, *, auth=True, body=None, idem=None, extra=None):
    url = BASE + path
    headers = {"Ubag-Api-Version": API, "Content-Type": "application/json"}
    if auth:
        headers["Authorization"] = "Bearer " + SECRET
    if idem:
        headers["Idempotency-Key"] = idem
    if extra:
        headers.update(extra)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        r = urllib.request.urlopen(req, timeout=15)
        return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
    except Exception as e:  # noqa: BLE001
        return -1, str(e)


def code_of(payload):
    try:
        return json.loads(payload).get("error", {}).get("code", "")
    except Exception:  # noqa: BLE001
        return ""


def record(name, status, payload, expect=None):
    err = code_of(payload)
    ok = (expect is None) or (status in expect)
    results.append((name, status, err, ok))


JOB_BODY = {
    "job": {"target": "mock", "command_type": "chat.send", "input": {"prompt": "e2e"}},
    "client": {"app_id": "e2e", "app_version": "1.0.0", "sdk": {"name": "probe", "version": "1.0.0"}},
}

print(f"=== UBAG E2E probe | role={ROLE} | base={BASE} ===")

# --- unauthenticated / health surface ---
s, p = call("GET", "/v1/health", auth=False)
record("health (no auth)", s, p, {200})
s, p = call("GET", "/v1/ready", auth=False)
record("ready (no auth)", s, p, {200})
s, p = call("GET", "/v1/version", auth=False)
record("version (no auth)", s, p, {200})

# --- auth rejection ---
s, p = call("POST", "/v1/jobs", auth=False, body=JOB_BODY, idem="e2e-noauth-key-00001")
record("jobs POST (no token) -> 401", s, p, {401})
s, p = call("POST", "/v1/jobs", body=JOB_BODY, idem="e2e-badtok-key-00001",
            extra={"Authorization": "Bearer not-the-real-secret"})
record("jobs POST (bad token) -> 401", s, p, {401})

# --- job lifecycle ---
s, p = call("POST", "/v1/jobs", body=JOB_BODY, idem=f"e2e-{ROLE}-create-0001")
record("jobs POST (job:create)", s, p)
job_id = ""
try:
    job_id = json.loads(p).get("job_id", "")
except Exception:  # noqa: BLE001
    pass

# idempotency replay (same key -> same job)
s, p = call("POST", "/v1/jobs", body=JOB_BODY, idem=f"e2e-{ROLE}-create-0001")
replay = ""
try:
    replay = str(json.loads(p).get("idempotent_replay"))
except Exception:  # noqa: BLE001
    pass
record(f"jobs POST replay (idempotent_replay={replay})", s, p)

s, p = call("GET", "/v1/jobs")
record("jobs GET list (job:read)", s, p)
if job_id:
    s, p = call("GET", f"/v1/jobs/{job_id}")
    record("jobs GET by id (job:read)", s, p)

# --- read-only catalogs (job:read) ---
for path in ("/v1/targets", "/v1/adapters", "/v1/templates", "/v1/workflows"):
    s, p = call("GET", path)
    record(f"GET {path} (job:read)", s, p)

# --- enterprise / privileged routes ---
s, p = call("GET", "/v1/cache")
record("GET /v1/cache (job:read)", s, p)
s, p = call("GET", "/v1/rate-limits")
record("GET /v1/rate-limits (rate_limit:manage)", s, p)
s, p = call("GET", "/v1/audit")
record("GET /v1/audit (audit:read)", s, p)
s, p = call("POST", "/v1/audit/export", body={"format": "ndjson"}, idem=f"e2e-{ROLE}-export-0001")
record("POST /v1/audit/export (data:export)", s, p)
s, p = call("GET", "/v1/sso/config")
record("GET /v1/sso/config (role:manage)", s, p)
s, p = call("POST", "/v1/webhooks/secret:rotate", body={}, idem=f"e2e-{ROLE}-rotate-0001")
record("POST /v1/webhooks/secret:rotate (secret:rotate)", s, p)

# --- print table ---
print(f"{'route':<46}{'status':>7}  {'error_code':<34}")
print("-" * 92)
for name, status, err, _ok in results:
    print(f"{name:<46}{status:>7}  {err:<34}")

denied = sum(1 for _n, st, _e, _o in results if st == 403)
print(f"\nrole={ROLE}: {len(results)} routes probed, {denied} returned 403 (RBAC denied)")
