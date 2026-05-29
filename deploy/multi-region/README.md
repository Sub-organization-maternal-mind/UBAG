# UBAG Multi-Region (Enterprise Profile)

Two-region **active/active** topology for the enterprise profile: per-region
gateway + Postgres + NATS + MinIO, a NATS supercluster spanning regions, MinIO
site replication for artifacts, and a global edge that load-balances and fails
over between regions.

## Files

```
deploy/multi-region/
├── docker-compose.multiregion.yml     # 2-region topology demo on one host
├── env.example                        # placeholders (copy to env.local)
├── caddy/Caddyfile.global             # global edge LB + active health checks
├── nats/nats-a.conf                   # region A NATS + supercluster gateway
├── nats/nats-b.conf                   # region B NATS + supercluster gateway
├── helm-values/
│   ├── values-region-a.yaml           # Helm overlay for region A cluster
│   └── values-region-b.yaml           # Helm overlay for region B cluster
└── README.md
```

## Compose demo (single host, two regions)

```powershell
Copy-Item deploy\multi-region\env.example deploy\multi-region\env.local
# edit env.local: replace every placeholder
docker compose -f deploy/multi-region/docker-compose.multiregion.yml `
  --env-file deploy/multi-region/env.local config        # validate
docker compose -f deploy/multi-region/docker-compose.multiregion.yml `
  --env-file deploy/multi-region/env.local up -d --build  # run
```

The global edge listens on `http://127.0.0.1:8080/v1/*` and distributes requests
across `gateway-a` and `gateway-b` with active `/v1/ready` health checks. Stop
one region's gateway to see automatic failover to the other.

> The Compose file is a **topology demo** to validate config and the failover
> path on one machine. It does not provide true geographic isolation.

## Kubernetes (real multi-region)

Deploy the Helm chart once per regional cluster, layering the region overlay:

```bash
helm upgrade --install ubag deploy/helm/ubag \
  -f deploy/helm/ubag/values-production.yaml \
  -f deploy/multi-region/helm-values/values-region-a.yaml \
  --kube-context region-a --namespace ubag --create-namespace

helm upgrade --install ubag deploy/helm/ubag \
  -f deploy/helm/ubag/values-production.yaml \
  -f deploy/multi-region/helm-values/values-region-b.yaml \
  --kube-context region-b --namespace ubag --create-namespace
```

Or drive both from Argo CD `ApplicationSet`
(`deploy/gitops/argocd/applicationset.yaml`).

## Data-tier replication

### Postgres logical replication

Each region runs Postgres with `wal_level=logical` (already set in the Compose
demo). To replicate gateway tables between regions:

```sql
-- On region A (publisher):
CREATE PUBLICATION ubag_pub FOR ALL TABLES;

-- On region B (subscriber):
CREATE SUBSCRIPTION ubag_sub
  CONNECTION 'host=postgres-a dbname=ubag user=ubag password=... sslmode=require'
  PUBLICATION ubag_pub;
```

- **Active/active caveat:** plain logical replication is single-writer per
  table. For true active-active, pin tenants to a **home region** (writes go to
  the home region; the other region serves reads) or adopt `pgactive` /
  bidirectional replication with conflict handling. The blueprint calls out
  `pgactive` for workloads that tolerate active-active.
- Bootstrap the subscriber after schema migrations are applied in both regions.

### NATS supercluster

`nats-a.conf` / `nats-b.conf` define a per-region cluster plus a `gateway{}`
block linking them into a supercluster. JetStream uses per-region `domain`s
(`region-a`, `region-b`) so streams are region-local but subjects/consumers are
reachable cross-region. In production, run ≥3 NATS nodes per region for quorum
and connect leaf nodes/gateways over mTLS (`deploy/mtls/nats/nats-mtls.conf`).

### MinIO site replication

Artifacts are replicated bucket-for-bucket across regional MinIO sites:

```bash
mc alias set a https://minio-a:9000 $ROOT_USER $ROOT_PASS
mc alias set b https://minio-b:9000 $ROOT_USER $ROOT_PASS
mc admin replicate add a b           # active-active site replication
mc admin replicate status a
```

Site replication syncs buckets, objects, IAM, and bucket settings both ways.

## Failover & DR

| Failure | Detection | Action |
| --- | --- | --- |
| Single gateway pod | edge/ingress health check | LB removes it; replicas absorb load |
| Whole region gateway tier | global LB `/v1/ready` checks | traffic shifts to the healthy region |
| Region Postgres primary | app readiness + monitoring | promote the other region's instance (or replica) to writer; repoint DSN |
| Region NATS | supercluster gateway loss | other region's NATS keeps serving; messages converge on heal |
| Region MinIO | site replication lag/health | reads/writes served by the healthy site; reconcile on recovery |

**RPO/RTO:** governed by replication lag (Postgres logical + MinIO site
replication are asynchronous → non-zero RPO). Document target RPO/RTO per
tenant. Run periodic failover game-days.

## Requires external infra (production)

- Separate clusters/hosts in distinct regions.
- GeoDNS / global load balancer with health-based routing.
- Real TLS/mTLS certificates (`deploy/mtls`).
- Postgres logical replication / `pgactive` bootstrap (manual, above).
- MinIO site replication bootstrap (manual, above).
- ≥3 NATS nodes per region for quorum.

## Validates offline

- `docker compose -f deploy/multi-region/docker-compose.multiregion.yml --env-file deploy/multi-region/env.example config`
- `helm template` with the region overlays (no cluster needed).
