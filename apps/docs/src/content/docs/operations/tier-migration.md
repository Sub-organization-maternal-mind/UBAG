---
title: Tier Migration
description: Runbook for migrating a UBAG deployment between tiers using `ubag migrate`, including prerequisites per tier and custom Caddy build steps.
---

# Tier Migration

This runbook covers upgrading a UBAG deployment from one tier to the next using `ubag migrate`. Downgrades are not supported by the migration command and require a manual procedure described at the end of this document.

---

## Overview

UBAG supports three deployment tiers:

```
edge  →  small  →  enterprise
```

Migrations are **upgrade-only** and **additive**: each tier builds on the infrastructure of the previous tier. The `ubag migrate` command handles schema migrations, configuration updates, and validation. It does not provision external infrastructure (Postgres, NATS, Kubernetes); operators must provision these before running the command.

---

## Prerequisites by Target Tier

### Migrating to `small`

Before running `ubag migrate --to small`:

- [ ] Postgres 15+ is running and accessible from the UBAG host.
- [ ] A database and user have been created: `CREATE DATABASE ubag; CREATE USER ubag WITH PASSWORD '...';`
- [ ] The Postgres DSN is available: `UBAG_DB_DSN=postgres://ubag:password@host:5432/ubag?sslmode=require`
- [ ] NATS 2.10+ (single node, JetStream enabled) is running.
- [ ] `UBAG_NATS_URL` is set to the NATS server address.
- [ ] Docker Engine 24+ and Docker Compose v2 are installed (for Compose-based small deployments).
- [ ] A custom Caddy binary is built (see [Custom Caddy Build](#custom-caddy-build) below).
- [ ] The Compose files at `deploy/compose/` are available on the target host.

### Migrating to `enterprise`

Before running `ubag migrate --to enterprise`:

- [ ] All `small` prerequisites are satisfied.
- [ ] Kubernetes 1.28+ cluster is accessible via `kubectl`.
- [ ] `helm` 3.14+ is installed.
- [ ] A Postgres HA instance (CloudNativePG, RDS Multi-AZ, Cloud SQL HA, etc.) is running and the DSN is available.
- [ ] A NATS cluster (3+ nodes, JetStream enabled) is running.
- [ ] Container registry credentials are configured in the cluster (image pull secret or registry mirror).
- [ ] Terraform modules at `deploy/terraform/<cloud>/` have been applied to provision cloud infrastructure.
- [ ] Helm chart at `deploy/helm/ubag` is available.

---

## Running the Migration

### Dry run (always do this first)

```sh
ubag migrate --to small --dry-run
```

The dry-run outputs a summary of schema migrations, configuration changes, and validation checks that will be performed. No changes are applied.

### Apply the migration

```sh
ubag migrate --to small
```

The command:

1. Validates that the target tier is higher than the current tier.
2. Applies all pending schema migrations (idempotent, transactional DDL).
3. Updates the local UBAG configuration to reflect the new tier.
4. Runs post-migration validation checks.
5. Prints a summary of applied changes.

### Makefile shortcut

```sh
# Dry run
make migrate-tier TO=small DRY_RUN=--dry-run

# Apply
make migrate-tier TO=small

# With explicit source tier
make migrate-tier TO=enterprise FROM=small
```

---

## Custom Caddy Build

The `small` and `enterprise` tiers require a custom Caddy binary built with `xcaddy`. The standard upstream Caddy binary does not include the required modules.

### Required modules

- `github.com/mholt/caddy-ratelimit` — per-IP and per-route rate limiting.
- `github.com/corazawaf/coraza-caddy` — OWASP Core Rule Set WAF.

### Build steps

**Install xcaddy:**

```sh
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
```

**Build the custom binary:**

```sh
xcaddy build \
  --with github.com/mholt/caddy-ratelimit \
  --with github.com/corazawaf/coraza-caddy
```

This produces a `caddy` binary in the current directory.

**Place the binary:**

- For Compose deployments: copy `caddy` to `deploy/caddy/caddy` and rebuild the Caddy container image, or mount it as a volume bind.
- For Kubernetes: bake it into a custom container image derived from `caddy:<version>` and push to your registry.

**Validate the build:**

```sh
make nginx-validate
# or: node tools/check-nginx-dashboard.mjs
```

The validator checks that the Caddy configuration file (`deploy/caddy/Caddyfile` or equivalent) only references directives and modules present in the expected custom build.

---

## Rollback

### Rolling back a failed migration

If `ubag migrate` fails mid-migration:

1. Check the migration log for the last successfully applied step.
2. The migration is transactional; a failed step rolls back the current schema transaction automatically.
3. Fix the prerequisite that caused the failure (e.g., missing Postgres connection, insufficient permissions).
4. Re-run `ubag migrate --to <tier>`. Applied migrations are idempotent and are skipped on re-run.

### Downgrade (not supported by `ubag migrate`)

Downgrading to a lower tier requires a manual procedure. The `ubag migrate` command refuses downgrade requests.

**Manual downgrade procedure:**

1. **Export data from the higher tier.**
   - For Postgres: `pg_dump --format=custom ubag > ubag-export.dump`
   - Run `ubag backup` to capture application-level state.

2. **Decommission higher-tier infrastructure.** This is environment-specific. Refer to your cloud provider or on-premises procedures to decommission Postgres, NATS cluster, Kubernetes cluster, etc.

3. **Install the target tier from scratch.** Follow the installation instructions in `docs/operations/deployment.md` for the target tier. Do not attempt to reuse the previous installation.

4. **Restore data.** Use `ubag restore --from <backup>` or `pg_restore` as appropriate for the target tier.

5. **Verify.** Run `ubag migrate --to <tier> --dry-run` to confirm the installation is at the expected schema version.

---

## Troubleshooting

### `ubag migrate` fails with "downgrade not supported"

The target tier is lower than or equal to the current tier. Check the current tier with `ubag status`. To migrate to a lower tier, follow the manual downgrade procedure above.

### `ubag migrate` fails with "connection refused" or "no such host"

A required external service (Postgres, NATS) is not reachable. Verify connectivity with:

```sh
psql "$UBAG_DB_DSN" -c '\conninfo'
nats server info --server "$UBAG_NATS_URL"
```

### Caddy fails to start after migration

The Caddy binary may be the standard upstream build, not the custom xcaddy build. Check:

```sh
caddy version
caddy list-modules | grep -E "ratelimit|coraza"
```

If the modules are not listed, rebuild using the steps in [Custom Caddy Build](#custom-caddy-build).

### Helm install fails with "image pull error"

Container registry credentials may not be configured in the cluster. Create an image pull secret:

```sh
kubectl create secret docker-registry ubag-registry \
  --docker-server=ghcr.io \
  --docker-username=<user> \
  --docker-password=<token> \
  --namespace=ubag
```

Then set `imagePullSecrets` in the Helm values.

---

## Reference

- `ubag migrate --help` — command options.
- `ADR 0012` — upgrade-only tier migration decision rationale.
- `docs/operations/deployment.md` — per-tier installation and configuration.
- `make migrate-tier TO=<tier> DRY_RUN=--dry-run` — Makefile shortcut with dry-run.
