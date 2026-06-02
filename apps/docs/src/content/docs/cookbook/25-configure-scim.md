---
title: Configure SCIM Provisioning
description: Automate user provisioning and deprovisioning with SCIM 2.0 via your identity provider.
---

UBAG implements SCIM 2.0 for automated user lifecycle management. Integrate it with Okta,
Microsoft Entra ID, or any SCIM-compatible IdP.

## Enable SCIM

In `ubag.toml`:

```toml
[scim]
enabled = true
base_url = "https://gateway.example.com/scim/v2"
bearer_token = "${UBAG_SCIM_TOKEN}"  # Generate via the dashboard
```

## Generate a SCIM token

```bash
curl -X POST http://localhost:8081/v1/scim/tokens \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -H "Idempotency-Key: $(uuidgen)"
# { "token": "scim_..." }
```

## Configure in Okta

1. Go to Applications → Your App → Provisioning
2. Set SCIM connector base URL: `https://gateway.example.com/scim/v2`
3. Authentication mode: HTTP Header with the SCIM token
4. Enable: Push Users, Push Groups

## SCIM endpoints

| Method | Path | Action |
|--------|------|--------|
| GET | `/scim/v2/Users` | List users |
| POST | `/scim/v2/Users` | Create user |
| PUT | `/scim/v2/Users/{id}` | Replace user |
| PATCH | `/scim/v2/Users/{id}` | Update user |
| DELETE | `/scim/v2/Users/{id}` | Deprovision user |
| GET | `/scim/v2/Groups` | List groups |
| POST | `/scim/v2/Groups` | Create group |
| PATCH | `/scim/v2/Groups/{id}` | Update group |

## Role mapping via groups

Map IdP groups to UBAG roles in `ubag.toml`:

```toml
[scim.group_mappings]
"ubag-admins"  = "admin"
"ubag-editors" = "editor"
"ubag-viewers" = "viewer"
```

See [SSO Sessions and Logout](/security/sso-sessions) for session management.
See [RBAC and ABAC](/security/rbac-abac) for the role permission model.
