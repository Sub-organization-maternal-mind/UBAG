---
title: Migrate Between Deployment Tiers
description: How to move from the small/compose deployment tier to the enterprise or multi-region tier.
---

UBAG ships three deployment profiles: `small` (single compose), `standard` (K8s), and
`enterprise` (multi-region with pgactive). Migrations are additive — no data loss required.

## Small → Standard

1. Export your current data:

```bash
ubag-cli export --format postgres-dump > ubag-backup.sql
```

2. Deploy the standard Helm chart:

```bash
helm repo add ubag https://charts.ubag.io
helm install ubag ubag/ubag-gateway \
  --set profile=standard \
  --set postgres.connectionString=$POSTGRES_URL
```

3. Import the backup:

```bash
ubag-cli import --file ubag-backup.sql --gateway https://gateway.example.com
```

4. Update DNS / load balancer to point to the new cluster.

5. Verify:

```bash
curl -H "Authorization: Bearer $UBAG_APP_SECRET" \
     -H "Ubag-Api-Version: 2026-05-22" \
     https://gateway.example.com/v1/health
```

## Standard → Enterprise (multi-region)

See [Multi-Region Operations](/operations/multi-region) and [Tier Migration](/operations/tier-migration) for the full runbook.

Key differences in the enterprise tier:

| Feature | Standard | Enterprise |
|---------|----------|-----------|
| Postgres | Single primary | pgactive multi-master |
| NATS | Single cluster | NATS JetStream with geo-replication |
| Auth | Basic SSO | SSO + MFA + SCIM |
| Regions | 1 | 3+ |

## Rollback

All migration steps are reversible. The original deployment remains active until DNS is cut over.
