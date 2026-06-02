---
title: Operator Guide
description: Deploy, configure, monitor, and maintain a UBAG gateway installation.
---

This guide covers everything an operator needs to run UBAG in production: deployment,
configuration, monitoring, upgrades, and incident response.

## Deployment profiles

UBAG ships three profiles. Choose based on your scale and reliability requirements:

| Profile | When to use | Infrastructure |
|---------|-------------|----------------|
| `small` | Evaluation, small teams | Docker Compose |
| `standard` | Production single-region | Kubernetes + Helm |
| `enterprise` | Multi-region, compliance | K8s + pgactive + NATS cluster |

See [Deployment Profiles](/deployment/profiles) and [Migrate Between Tiers](/cookbook/08-migrate-tiers).

## Quick start (small profile)

```bash
git clone https://github.com/ubag/ubag
cd ubag
cp .env.example .env
# Edit .env with your config
docker compose --profile small up -d
```

## Configuration reference

All configuration lives in `ubag.toml` (file) or environment variables (`UBAG_*`):

```toml
[gateway]
port = 8081
log_level = "info"
api_version = "2026-05-22"

[auth]
app_secret = "${UBAG_APP_SECRET}"

[database]
url = "${UBAG_POSTGRES_URL}"
max_connections = 20

[nats]
url = "${UBAG_NATS_URL}"

[storage]
garage_endpoint = "${GARAGE_ENDPOINT}"
garage_access_key = "${GARAGE_ACCESS_KEY}"
garage_secret_key = "${GARAGE_SECRET_KEY}"
```

## Health and monitoring

- Health: `GET /v1/health`
- Metrics: `GET /v1/metrics` (Prometheus format)
- Grafana dashboards: imported automatically in the `small` profile

See [Monitor Gateway Health](/cookbook/30-health-check) and [Observability](/operations/observability).

## Upgrades

```bash
# 1. Check release notes
open https://github.com/ubag/ubag/releases

# 2. Run database migrations (non-destructive, backward-compatible)
ubag-cli migrate --gateway http://localhost:8081 --token $UBAG_APP_SECRET

# 3. Roll out the new gateway image
kubectl set image deployment/ubag-gateway gateway=ghcr.io/ubag/gateway:0.10.0

# 4. Verify
curl http://localhost:8081/v1/health
```

## Backup and restore

See [Back Up and Restore](/cookbook/12-backup-restore).

## Incident response

See [Runbook](/operations/runbook) and [Operator Runbook](/operations/operator-runbook).

## Security hardening

- Rotate app secrets regularly: [Rotate an App Secret](/cookbook/04-rotate-secret)
- Enable SSO + MFA: [Configure SSO](/cookbook/09-configure-sso), [Configure MFA](/cookbook/10-configure-mfa)
- Review audit log: [View Audit Log](/cookbook/20-view-audit-log)
- Configure SIEM: [Configure SIEM](/cookbook/32-configure-siem)

## Disaster recovery

See [Disaster Recovery](/operations/disaster-recovery) for RTO/RPO targets and runbooks by tier.
