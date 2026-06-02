---
title: "Enterprise Authentication"
description: MFA enrollment and verification, JIT admin elevation, SSO authorization-code flow, audit WORM and SIEM export, recovery code management.
---

# Enterprise Authentication

This document covers UBAG's enterprise authentication and authorization features: TOTP-based multi-factor authentication, just-in-time admin elevation, SSO via OIDC authorization-code flow, immutable audit (WORM) storage, and SIEM streaming.

All features in this document require the `enterprise` profile or `UBAG_ENABLE_GEO_REPLICATION=1`.

---

## MFA Enrollment and Verification

UBAG implements TOTP-based MFA natively (`internal/mfa/`). The IdP (e.g., Keycloak) authenticates the principal's password credential; UBAG gates its own privileged actions behind a second factor.

### Enrollment

```http
POST /v1/mfa/enroll
Authorization: Bearer <user-token>
```

**Response:**
```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "otpauth_uri": "otpauth://totp/UBAG:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=UBAG",
  "recovery_codes": ["AAAA-BBBB", "CCCC-DDDD", "EEEE-FFFF", "GGGG-HHHH", "IIII-JJJJ",
                     "KKKK-LLLL", "MMMM-NNNN", "OOOO-PPPP"]
}
```

Scan the `otpauth_uri` with an authenticator app (e.g., Google Authenticator, Authy, Bitwarden). Store the `recovery_codes` securely — they are shown only once.

### Completing enrollment

After scanning, confirm enrollment by submitting a TOTP code:

```http
POST /v1/mfa/verify
Authorization: Bearer <user-token>
Content-Type: application/json

{
  "code": "123456"
}
```

A `200 OK` response confirms enrollment is active. Subsequent privileged actions will require `X-MFA-Token: <totp-code>` in the request header.

### MFA-gated actions

The following capabilities require a valid TOTP code on every request:

| Capability | Example action |
|---|---|
| `role:manage` | Create/delete roles, assign permissions |
| `data:export` | Bulk export of job data, audit logs |
| `region:manage` | Kill-switch state transitions, home-region pin updates |

To include the MFA token in a request:

```http
POST /v1/admin/regions/region-a/state
Authorization: Bearer <admin-token>
X-MFA-Token: 123456
Content-Type: application/json

{"state": "draining"}
```

---

## Recovery Code Management

Recovery codes are one-time-use codes for MFA account recovery when the TOTP device is unavailable.

### Using a recovery code

```http
POST /v1/mfa/verify
Authorization: Bearer <user-token>
Content-Type: application/json

{
  "recovery_code": "AAAA-BBBB"
}
```

Each code may be used exactly once. After use, the code is invalidated.

### Regenerating recovery codes

If recovery codes are lost or exhausted, regenerate them (requires active TOTP verification):

```http
POST /v1/mfa/recovery/regenerate
Authorization: Bearer <user-token>
X-MFA-Token: 123456
```

**Response:** A new set of 8 recovery codes. Previous codes are invalidated immediately.

### Revoking MFA

An admin may revoke a user's MFA enrollment (requires `role:manage` + MFA):

```http
DELETE /v1/admin/users/{userID}/mfa
Authorization: Bearer <admin-token>
X-MFA-Token: 123456
```

After revocation, the user must re-enroll before performing privileged actions.

---

## JIT Admin Elevation

Just-in-time (JIT) elevation grants a user a time-boxed set of elevated capabilities, subject to a second approver. Elevation grants expire automatically (default 30 minutes, configurable per tenant).

### Requesting elevation

```http
POST /v1/admin/elevation
Authorization: Bearer <user-token>
X-MFA-Token: 123456
Content-Type: application/json

{
  "capabilities": ["role:manage", "region:manage"],
  "reason": "Scheduled region maintenance window",
  "duration_minutes": 60
}
```

**Response:**
```json
{
  "elevation_id": "elev_01JXK9...",
  "status": "pending_approval",
  "capabilities": ["role:manage", "region:manage"],
  "expires_at": null,
  "requested_by": "alice@example.com",
  "requested_at": "2026-06-02T10:00:00Z"
}
```

The request is in `pending_approval` state until an approver acts on it.

### Approving an elevation request

A second admin (with `role:manage` capability and MFA) approves the request:

```http
POST /v1/admin/elevation/{elevationID}/approve
Authorization: Bearer <approver-token>
X-MFA-Token: 654321
```

**Response:**
```json
{
  "elevation_id": "elev_01JXK9...",
  "status": "active",
  "capabilities": ["role:manage", "region:manage"],
  "expires_at": "2026-06-02T11:00:00Z"
}
```

Once `active`, the requesting user's token includes the elevated capabilities until `expires_at`.

### Rejecting a request

```http
POST /v1/admin/elevation/{elevationID}/reject
Authorization: Bearer <approver-token>
X-MFA-Token: 654321
Content-Type: application/json

{
  "reason": "Not scheduled for today"
}
```

### Revoking an active elevation

An admin may revoke an active elevation at any time:

```http
DELETE /v1/admin/elevation/{elevationID}
Authorization: Bearer <admin-token>
X-MFA-Token: 123456
```

### Elevation audit trail

Every elevation lifecycle event (request, approve, reject, expire, revoke) is written to the immutable audit log and streamed to SIEM sinks.

---

## SSO Authorization-Code Flow (OIDC)

UBAG supports OIDC authorization-code flow for SSO with any compliant IdP (Keycloak, Okta, Azure AD, Google Workspace).

### Configuration

```bash
UBAG_SSO_ISSUER=https://auth.example.com/realms/ubag
UBAG_SSO_CLIENT_ID=ubag-gateway
UBAG_SSO_CLIENT_SECRET=<client-secret>
UBAG_SSO_REDIRECT_URI=https://gateway.example.com/v1/auth/callback
UBAG_SSO_SCOPES=openid,profile,email
```

### Flow

1. **Initiate:** The client redirects the user to:
   ```
   GET /v1/auth/login?redirect_to=<post-login-url>
   ```
   UBAG generates a `state` parameter (opaque, CSRF token) and a `nonce` (bound to the session), stores them in an encrypted cookie, and redirects to the IdP authorization endpoint.

2. **IdP authentication:** The user authenticates at the IdP. The IdP redirects back to:
   ```
   GET /v1/auth/callback?code=<auth-code>&state=<state>
   ```

3. **Token exchange:** UBAG validates the `state` against the cookie, exchanges the `code` for `id_token` and `access_token` at the IdP token endpoint, validates the `nonce` in the `id_token`, and creates a UBAG session.

4. **Session:** A signed UBAG session cookie is set. The user is redirected to the original `redirect_to` URL.

### Identity mapping

The IdP `sub` claim is mapped to a UBAG `user_id`. The `email` claim is used as a display name. Group claims (configurable via `UBAG_SSO_GROUPS_CLAIM`) map to UBAG roles.

### Keycloak setup (reference)

A reference Keycloak configuration for local SSO integration testing is at `apps/gateway/tests/sso/docker-compose.yml`. Start it with:

```bash
docker compose -f apps/gateway/tests/sso/docker-compose.yml up -d keycloak
```

Keycloak is preconfigured with the `ubag` realm, the `ubag-gateway` client, and a test user `testuser / testpass`.

---

## Audit WORM Storage

The audit subsystem (`internal/audit/`) provides an append-only, hash-chained audit log. The database user running the gateway has `INSERT`-only grants on the audit table — no `UPDATE` or `DELETE` is possible.

### Tamper-evidence verification

The audit log uses a SHA-256 hash chain: each record's hash is computed over `(previous_hash || event_payload)`. Periodic anchor snapshots (`SealHead`) are stored off-table (in NATS KV or a separate audit_seals table) to allow verification without reading the entire log.

Verify the audit chain:

```bash
ubag audit verify --from <timestamp> --to <timestamp>
```

A clean verification prints `audit chain OK: N records verified` and exits 0. Any gap or tampered record causes a non-zero exit with the offending record range.

### Querying the audit log

```http
GET /v1/admin/audit?from=2026-06-01T00:00:00Z&to=2026-06-02T00:00:00Z&actor=alice@example.com
Authorization: Bearer <admin-token>
X-MFA-Token: 123456
```

The `data:export` capability is required for bulk audit queries. Individual audit events for a user's own actions do not require `data:export`.

---

## SIEM Streaming Configuration

UBAG streams audit events to external SIEM systems via `internal/siem/`. Sinks are configured via environment variables and activated at gateway startup.

### Supported sinks

| Sink | Environment variable prefix | Protocol |
|---|---|---|
| Splunk HEC | `UBAG_SIEM_SPLUNK_*` | HTTPS POST |
| Elastic Beats | `UBAG_SIEM_ELASTIC_*` | HTTPS POST (ECS format) |
| Syslog | `UBAG_SIEM_SYSLOG_*` | RFC 5424 over TCP/TLS |

### Splunk HEC configuration

```bash
UBAG_SIEM_SPLUNK_ENABLED=true
UBAG_SIEM_SPLUNK_URL=https://splunk.example.com:8088
UBAG_SIEM_SPLUNK_TOKEN=<hec-token>
UBAG_SIEM_SPLUNK_INDEX=ubag_audit
UBAG_SIEM_SPLUNK_SOURCE_TYPE=ubag:audit
```

### Elastic configuration

```bash
UBAG_SIEM_ELASTIC_ENABLED=true
UBAG_SIEM_ELASTIC_URL=https://elastic.example.com:9200
UBAG_SIEM_ELASTIC_API_KEY=<api-key>
UBAG_SIEM_ELASTIC_INDEX=ubag-audit
```

### Syslog configuration

```bash
UBAG_SIEM_SYSLOG_ENABLED=true
UBAG_SIEM_SYSLOG_ADDR=syslog.example.com:6514
UBAG_SIEM_SYSLOG_NETWORK=tcp+tls
UBAG_SIEM_SYSLOG_CA_FILE=/etc/ubag/syslog-ca.crt
```

### Sink failure behavior

Each SIEM sink write has a 5-second deadline. If a sink fails, the failure is recorded in:
- Prometheus counter: `ubag_siem_export_errors_total{sink="splunk"}` (or `elastic`, `syslog`)
- The local audit log (the WORM chain is unaffected; only the SIEM fan-out fails)

A sink failure does **not** fail the originating request. Configure alerting on `ubag_siem_export_errors_total` to detect sustained sink outages.

### Verifying SIEM delivery

```bash
ubag siem test --sink splunk
```

This sends a synthetic test event to the configured sink and reports success or failure with the HTTP response body.
