---
title: "Multi-Region Operations"
description: Deploying UBAG across multiple regions — NATS supercluster, home-region pinning, kill-switch operation, GeoDNS, Garage object storage, and DR runbook.
---

# Multi-Region Operations

This runbook covers deploying and operating UBAG in a multi-region configuration. All multi-region features are gated by the `enterprise` profile or `UBAG_ENABLE_GEO_REPLICATION=1`. Single-region deployments are unaffected.

Reference deployment artifacts are under `deploy/multi-region/`.

---

## Prerequisites

- UBAG enterprise profile active (`UBAG_PROFILE=enterprise`) or `UBAG_ENABLE_GEO_REPLICATION=1`
- Two or more regions, each with a NATS cluster of at least three nodes
- pgactive extension installed on all PostgreSQL nodes
- Garage (or MinIO) nodes reachable from each region
- TLS certificates for NATS mTLS (see [NATS supercluster configuration](#nats-supercluster-configuration))

---

## Deploying Multi-Region

The reference Docker Compose stack for a two-region deployment is at `deploy/multi-region/docker-compose.multiregion.yml`. It defines two UBAG gateway instances (`gateway-region-a`, `gateway-region-b`), a two-region NATS supercluster, a pgactive PostgreSQL pair, and a Garage object-store cluster.

### Step 1 — Generate mTLS certificates

All inter-region NATS gateway and leaf-node connections require mutual TLS. Use the helper script:

```bash
bash deploy/multi-region/nats/gen-certs.sh
```

This creates `deploy/multi-region/nats/certs/` with a self-signed CA, per-node server certificates, and a client certificate for leaf nodes.

### Step 2 — Start the stack

```bash
docker compose -f deploy/multi-region/docker-compose.multiregion.yml up -d
```

### Step 3 — Initialize pgactive replication

After both PostgreSQL nodes are healthy, run:

```bash
docker compose -f deploy/multi-region/postgres/docker-compose.pgactive.yml exec postgres-a \
  psql -U ubag -c "SELECT pgactive.pgactive_create_group(node_name := 'region-a', node_dsn := 'host=postgres-a dbname=ubag');"

docker compose -f deploy/multi-region/postgres/docker-compose.pgactive.yml exec postgres-b \
  psql -U ubag -c "SELECT pgactive.pgactive_join_group(node_name := 'region-b', node_dsn := 'host=postgres-a dbname=ubag', join_using_dsn := 'host=postgres-a dbname=ubag');"
```

See `deploy/multi-region/postgres/pgactive.md` for full configuration details, conflict policy, and the `UBAG_POSTGRES_WRITE_REGION` environment variable.

### Step 4 — Apply the Phase 9 database migration

```bash
ubag migrate --to enterprise
```

This adds the `home_region TEXT NULL` column to `gateway_tenants` (see [Home-region pinning](#home-region-pinning)).

---

## Home-Region Pinning

A tenant may be pinned to a specific region so that all write operations for that tenant are accepted only by the named region. Read operations may be served by any region with an in-region replica.

### Schema

The pin is stored in `gateway_tenants.home_region TEXT NULL`. A `NULL` value means the tenant is unpinned — any region may accept writes.

### Setting a pin via the API

```http
PATCH /v1/admin/tenants/{tenantID}
Content-Type: application/json
Authorization: Bearer <admin-token>

{
  "home_region": "region-a"
}
```

This endpoint requires the `role:manage` capability and MFA verification. See [enterprise-auth.md](./enterprise-auth.md).

### Removing a pin

```http
PATCH /v1/admin/tenants/{tenantID}
Content-Type: application/json

{
  "home_region": null
}
```

Setting `home_region` to `null` restores the unpinned (any-region) behavior.

### Write-fence enforcement

When a write-class request arrives at a region that is not the tenant's home region, the gateway responds with:

```
HTTP 307 Temporary Redirect
Location: https://<home-region-endpoint>/v1/...
```

Clients and SDKs must follow this redirect. The Go and TypeScript SDKs follow 307 redirects automatically.

---

## Kill-Switch Operation

The kill switch controls whether a region accepts new jobs and participates in the load-balancer rotation.

### States

| State | New job submissions | In-flight jobs | `/v1/ready` | LB behavior |
|---|---|---|---|---|
| `active` | Accepted | Continue | HTTP 200 | In rotation |
| `draining` | Rejected (HTTP 503 + `Retry-After`) | Continue | HTTP 200 | In rotation (read/status only) |
| `disabled` | Rejected | Continue until complete | HTTP 503 | Removed from rotation |

### Transitioning state

```http
POST /v1/admin/regions/{region}/state
Content-Type: application/json
Authorization: Bearer <admin-token>
X-MFA-Token: <totp-code>

{
  "state": "draining"
}
```

This endpoint requires the `region:manage` capability and MFA verification.

### Graceful drain procedure

1. Set the region to `draining`:
   ```bash
   curl -X POST https://<gateway>/v1/admin/regions/region-a/state \
     -H "Authorization: Bearer $TOKEN" \
     -H "X-MFA-Token: $TOTP" \
     -d '{"state":"draining"}'
   ```
2. Monitor in-flight jobs until the queue drains:
   ```bash
   watch -n 5 'curl -s https://<gateway>/v1/admin/regions/region-a/status | jq .inflight_count'
   ```
3. Once `inflight_count` reaches 0, set the region to `disabled`:
   ```bash
   curl -X POST https://<gateway>/v1/admin/regions/region-a/state \
     -H "Authorization: Bearer $TOKEN" \
     -H "X-MFA-Token: $TOTP" \
     -d '{"state":"disabled"}'
   ```
4. The load balancer will detect the failing `/v1/ready` probe and shed the region within one health-check interval.

### Kill-switch drill (quarterly)

Run this drill in a non-production environment to validate the kill-switch path end-to-end:

1. Confirm `region-a` is `active` and serving traffic.
2. Set `region-a` to `draining`. Verify that new job submissions to `region-a` receive HTTP 503 and that `/v1/ready` still returns 200.
3. Set `region-a` to `disabled`. Verify that `/v1/ready` returns 503 and that the LB health check fails within the expected interval.
4. Set `region-a` back to `active`. Verify that `/v1/ready` returns 200 and the LB reintroduces the region.

---

## NATS Supercluster Configuration

The NATS supercluster connects region-local clusters via NATS gateway connections. Each region runs at least three NATS nodes for HA.

Reference configuration files are at `deploy/multi-region/nats/`:

| File | Purpose |
|---|---|
| `nats-a.conf` | Region-A primary node (gateway block + cluster routes) |
| `nats-a-1.conf` | Region-A second node |
| `nats-a-2.conf` | Region-A third node |
| `nats-b.conf` | Region-B primary node |
| `nats-b-1.conf` | Region-B second node |
| `nats-b-2.conf` | Region-B third node |
| `leaf-node.conf` | Leaf-node template (for edge gateways) |

### mTLS setup

All NATS gateway connections (inter-region) and leaf-node connections use mTLS. Each node's `gateway {}` block and `leafnodes {}` block must reference:

```
tls {
  cert_file: "/certs/server.crt"
  key_file:  "/certs/server.key"
  ca_file:   "/certs/ca.crt"
  verify:    true
}
```

The `verify: true` flag enforces mutual authentication. Leaf nodes present the client certificate generated by `gen-certs.sh`.

### Subject import/export scopes

Each region's NATS JetStream stream is configured with a subject filter of `ubag.jobs.<region>.*.*` so that only that region's jobs are stored locally. Cross-region forwarding is handled by NATS gateway subject-interest propagation — no application-level forwarding is required.

---

## GeoDNS / Anycast Edge

Reference configuration is at `deploy/multi-region/geodns/`.

| File | Purpose |
|---|---|
| `README.md` | Overview of GeoDNS strategy and health check integration |
| `route53.tf` | AWS Route 53 latency-based routing with `/v1/ready` health checks |

### AWS Route 53 (recommended)

The `route53.tf` Terraform module creates:
- One Route 53 health check per region, polling `GET /v1/ready` every 10 seconds.
- One latency-based routing record per region so that clients are directed to the nearest healthy region.
- An SNS alarm that fires when a region health check fails for two consecutive periods.

Apply:
```bash
cd deploy/multi-region/geodns
terraform init && terraform apply
```

### Cloudflare Load Balancing (alternative)

See `deploy/multi-region/geodns/README.md` for Cloudflare Load Balancing configuration. The health check path is `/v1/ready` in both cases.

---

## Garage Object Storage

Garage is the recommended self-hosted S3-compatible object store for sovereign-cloud and air-gapped deployments.

Reference configuration is at `deploy/multi-region/garage/`.

### Setup

```bash
docker compose -f deploy/multi-region/garage/docker-compose.garage.yml up -d
```

This starts a three-node Garage cluster. After the nodes start, initialize the cluster layout:

```bash
# Get node IDs
docker exec garage-1 garage node id

# Assign zones and weights (adjust node IDs accordingly)
docker exec garage-1 garage layout assign -z region-a -c 1 <node-id-1>
docker exec garage-1 garage layout assign -z region-a -c 1 <node-id-2>
docker exec garage-1 garage layout assign -z region-b -c 1 <node-id-3>
docker exec garage-1 garage layout apply --version 1
```

Create the UBAG bucket:
```bash
docker exec garage-1 garage bucket create ubag-artifacts
docker exec garage-1 garage bucket allow --read --write ubag-artifacts --key ubag
```

### Configuring UBAG to use Garage

```bash
UBAG_ARTIFACT_BACKEND=garage
UBAG_GARAGE_ENDPOINTS=http://garage-1:3900,http://garage-2:3900,http://garage-3:3900
UBAG_GARAGE_ACCESS_KEY_ID=<key-id>
UBAG_GARAGE_SECRET_ACCESS_KEY=<secret>
UBAG_GARAGE_BUCKET=ubag-artifacts
UBAG_GARAGE_REPLICATION_FACTOR=2
```

The `UBAG_GARAGE_REPLICATION_FACTOR` controls how many nodes must acknowledge a write. Writes succeed when at least `ceil(factor / 2)` nodes respond.

---

## DR Runbook — Regional Failover

### Planned failover (maintenance window)

1. Pin all active tenants to the surviving region:
   ```bash
   # For each tenant:
   curl -X PATCH .../v1/admin/tenants/$ID \
     -d '{"home_region":"region-b"}'
   ```
2. Drain and disable the departing region (see [Kill-Switch Operation](#kill-switch-operation)).
3. Perform maintenance.
4. Re-enable the region (`state: active`).
5. Optionally re-distribute tenant pins.

### Unplanned failover (region failure)

1. Verify that the failed region's `/v1/ready` is returning non-200 (GeoDNS will have already shed it if health checks are configured).
2. If pgactive replication lag is acceptable, promote the surviving region's PostgreSQL node as primary write node by updating `UBAG_POSTGRES_WRITE_REGION`.
3. Update tenant `home_region` pins for tenants previously pinned to the failed region:
   ```bash
   psql -c "UPDATE gateway_tenants SET home_region = 'region-b' WHERE home_region = 'region-a';"
   ```
4. Monitor Prometheus alert `ubag_region_state{state="disabled"}` until the failed region recovers.
5. When the failed region is restored, set it to `draining` first, verify replication catch-up, then set to `active`.

### Kill-switch drills schedule

Run a full kill-switch drill (see [Kill-Switch Drill](#kill-switch-drill-quarterly)) on a quarterly basis in the staging environment. Document results in the incident log.
