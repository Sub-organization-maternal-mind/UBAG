---
title: Deployment
description: Per-tier install matrix, compose files, Helm chart, and Terraform modules for UBAG.
---

# Deployment

This page covers how to install and operate UBAG across its three deployment tiers. It is the authoritative reference for operators, not a developer quickstart.

---

## Deployment Tiers

UBAG ships three deployment tiers. Each tier is a superset of the previous.

| Tier | Target | Database | Queue | Kubernetes | HA |
|---|---|---|---|---|---|
| **edge** | Single machine, personal use | SQLite | File spool | No | No |
| **small** | Small team, self-hosted VPS | Postgres | NATS (single node) | Optional | Optional |
| **enterprise** | Organisation, cloud or on-prem | Postgres (HA) | NATS (cluster) | Required | Yes |

---

## Edge Tier

### Requirements

- Linux x86\_64, macOS, or Windows (WSL2 for production use).
- No external dependencies. SQLite and the file spool are embedded.

### Install

**Installer script (Linux/macOS):**

```sh
curl -fsSL https://install.ubag.io/edge | sh
```

**Windows (PowerShell):**

```powershell
irm https://install.ubag.io/edge.ps1 | iex
```

**Manual binary install:**

Download the `ubag_<version>_<os>_<arch>.tar.gz` asset from the GitHub Release and extract `ubag` to a directory in `$PATH`.

### Compose file

Edge tier does not use Docker Compose. Run the binary directly:

```sh
ubag start
```

This starts the gateway (HTTP), the Python worker subprocess, and the file-spool executor. All state is written to `~/.ubag/`.

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `UBAG_EXECUTOR_MODE` | `file` | Executor backend (`file` or `river`) |
| `UBAG_EXECUTOR_SPOOL_DIR` | `~/.ubag/spool` | File spool directory |
| `UBAG_DB_PATH` | `~/.ubag/ubag-gateway.db` | SQLite database path |
| `UBAG_LISTEN` | `:8080` | HTTP listen address |

---

## Small Tier

### Requirements

- Linux x86\_64 server (bare-metal or VPS).
- Docker Engine 24+ and Docker Compose v2.
- 2 vCPU, 4 GB RAM minimum.
- Postgres 15+ accessible from the server.
- Optional: a domain name with DNS pointed at the server for Caddy TLS.

### Compose files

Small-tier deployments use two compose files:

| File | Purpose |
|---|---|
| `deploy/compose/docker-compose.yml` | Core services: gateway, worker, NATS, Caddy |
| `deploy/compose/docker-compose.observability.yml` | Optional: Prometheus, Grafana, Loki, Tempo |

**Bring up core services:**

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
```

**Bring up with observability:**

```sh
docker compose \
  -f deploy/compose/docker-compose.yml \
  -f deploy/compose/docker-compose.observability.yml \
  up -d
```

**Bring up with WAL archiving (opt-in backup profile):**

```sh
docker compose \
  -f deploy/compose/docker-compose.yml \
  --profile backup \
  up -d
```

### Custom Caddy build

Small tier and above use a custom Caddy binary built with `xcaddy`. The standard Caddy upstream binary is not used.

**Modules required:**

- `github.com/mholt/caddy-ratelimit` — per-IP and per-route rate limiting.
- `github.com/corazawaf/coraza-caddy` — OWASP CRS WAF.

**Build the custom binary:**

```sh
# Install xcaddy if not present
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

# Build with required modules
xcaddy build \
  --with github.com/mholt/caddy-ratelimit \
  --with github.com/corazawaf/coraza-caddy
```

The resulting `caddy` binary should be placed at `deploy/caddy/caddy` or baked into the Caddy container image used in Compose.

**Validate the Caddy config:**

```sh
make caddy-validate
# or: node tools/check-caddy.mjs
```

### Configuration

Configure Postgres and NATS connection strings via environment variables or a `.env` file at the project root:

```env
UBAG_DB_DSN=postgres://ubag:password@postgres:5432/ubag?sslmode=require
UBAG_NATS_URL=nats://nats:4222
UBAG_EXECUTOR_MODE=river
```

---

## Enterprise Tier

### Requirements

- Kubernetes 1.28+ cluster.
- `kubectl` and `helm` 3.14+ installed locally.
- Terraform 1.7+ for cloud infrastructure provisioning.
- A container registry accessible from the cluster (e.g., `ghcr.io`).
- Postgres HA (e.g., CloudNativePG, AWS RDS Multi-AZ, GCP Cloud SQL HA).
- NATS cluster (JetStream enabled, 3+ nodes).

### Helm chart

The UBAG Helm chart is located at `deploy/helm/ubag`.

**Default values install (single-replica, non-HA):**

```sh
helm install ubag deploy/helm/ubag \
  --namespace ubag \
  --create-namespace \
  --set gateway.image.tag=<version>
```

**HA values install:**

```sh
helm install ubag deploy/helm/ubag \
  --namespace ubag \
  --create-namespace \
  -f deploy/helm/ubag/values-ha.yaml \
  --set gateway.image.tag=<version>
```

**Lint and template check:**

```sh
make helm-lint
# or:
helm lint deploy/helm/ubag
helm template deploy/helm/ubag -f deploy/helm/ubag/values-ha.yaml > /dev/null
```

**Key chart values:**

| Value | Default | Purpose |
|---|---|---|
| `gateway.replicaCount` | `1` | Gateway pod replicas |
| `gateway.image.repository` | `ghcr.io/ubag/gateway` | Container image |
| `worker.replicaCount` | `1` | Worker pod replicas |
| `nats.url` | `nats://nats:4222` | NATS connection |
| `postgres.dsn` | _(required)_ | Postgres DSN (use a Secret) |
| `caddy.enabled` | `true` | Deploy Caddy reverse proxy |
| `operator.enabled` | `false` | Deploy UBAG Kubernetes Operator |

### Kubernetes Operator

The UBAG Operator (`deploy/operator/`) manages `UBAGProfile` custom resources and reconciles gateway/worker deployments against desired profile state.

Enable it via the Helm chart:

```sh
helm upgrade ubag deploy/helm/ubag \
  --set operator.enabled=true \
  --set operator.image.tag=<version>
```

Or deploy the operator independently:

```sh
kubectl apply -f deploy/operator/config/crd/
kubectl apply -f deploy/operator/config/deploy/
```

### Terraform modules

Terraform modules for cloud infrastructure provisioning live under `deploy/terraform/`.

| Module | Cloud | Purpose |
|---|---|---|
| `deploy/terraform/aws/` | AWS | EKS cluster, RDS, ElastiCache, S3, Route53 |
| `deploy/terraform/gcp/` | GCP | GKE cluster, Cloud SQL, Memorystore, GCS, Cloud DNS |
| `deploy/terraform/azure/` | Azure | AKS cluster, Azure Database for Postgres, Azure Cache, Blob Storage |
| `deploy/terraform/hetzner/` | Hetzner | Dedicated servers, Hetzner Cloud LB, volumes |
| `deploy/terraform/digitalocean/` | DigitalOcean | DOKS cluster, managed Postgres, Spaces |
| `deploy/terraform/_shared/` | All | Shared variables, outputs, and provider version constraints |

**Validate all modules:**

```sh
make tf-validate
# or: node tools/check-enterprise-deploy.mjs
```

**Example: provision on AWS:**

```sh
cd deploy/terraform/aws
terraform init
terraform plan -out=tfplan
terraform apply tfplan
```

**Note:** Terraform provider authentication (AWS credentials, GCP service account, etc.) is managed outside this repository. See your cloud provider's documentation.

---

## GitOps

GitOps manifests for ArgoCD or Flux are located under `deploy/gitops/`. Validate manifests:

```sh
node tools/check-gitops.mjs
```

---

## Release

All releases are produced by goreleaser from `.goreleaser.yaml` at the repository root. Use the Makefile targets:

```sh
# Production release (requires GITHUB_TOKEN)
make release

# Local snapshot preview (no publish)
make release-snapshot
```

The `make release-snapshot` target is safe to run locally; it does not push to any registry or create a GitHub Release.

---

## Reference

- `make help` — list all Makefile targets.
- `ADR 0012` — architecture decisions for Phase 8 deployment infrastructure.
- `docs/operations/tier-migration.md` — runbook for migrating between tiers.
