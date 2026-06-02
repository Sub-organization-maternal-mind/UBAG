---
title: Configure SSO/OIDC
description: Enable single sign-on for the UBAG dashboard using an OIDC-compatible identity provider.
---

UBAG supports SSO via OIDC. Configure your identity provider and register the callback URL.

## Supported providers

- Okta, Auth0, Microsoft Entra ID (Azure AD)
- Any OIDC-compliant provider (Google Workspace, Keycloak, etc.)

## Gateway configuration

Add to your `ubag.toml` or environment:

```toml
[sso]
enabled = true
provider = "oidc"
issuer_url = "https://your-idp.example.com"
client_id = "ubag-gateway"
client_secret = "${UBAG_OIDC_CLIENT_SECRET}"
redirect_uri = "https://gateway.example.com/v1/sso/callback"
scopes = ["openid", "profile", "email", "groups"]
```

## Register UBAG in your IdP

Set the allowed redirect URI to:

```
https://<your-gateway-host>/v1/sso/callback
```

## Test the SSO flow

```bash
# Initiate SSO login (returns redirect URL)
curl http://localhost:8081/v1/sso/authorize \
  -H "Ubag-Api-Version: 2026-05-22"
```

## Claim mapping

Map IdP claims to UBAG roles via `claim_mappings`:

```toml
[sso.claim_mappings]
email = "email"
groups = "groups"
role_admin = "ubag-admins"
role_viewer = "ubag-viewers"
```

## Verify nonce (security)

The UBAG OIDC callback verifies the `nonce` claim to prevent replay attacks (ADR-0002, fix Task 2.4).
Ensure your IdP returns the nonce claim in the ID token.

See [SSO Sessions and Logout](/security/sso-sessions) for session management details.
