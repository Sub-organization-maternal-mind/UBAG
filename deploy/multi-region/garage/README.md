# Garage Geo-Replicated Object Storage

[Garage](https://garagehq.deuxfleurs.fr/) is a lightweight, S3-compatible distributed object store built for geo-distribution across datacenters and regions.

## Local Development Cluster

The `docker-compose.yml` in this directory starts a 3-node Garage cluster on your local machine for development and integration testing.

```bash
docker compose up -d
```

### Node Endpoints

| Node | S3 API | Admin API | RPC |
|------|--------|-----------|-----|
| garage-node-0 | :3900 | :3903 | :3901 |
| garage-node-1 | :3910 | :3913 | :3911 |
| garage-node-2 | :3920 | :3923 | :3921 |

### Bootstrap the Cluster

After the nodes are running, apply the cluster layout. Each node must be assigned to a zone and a capacity:

```bash
# Check node IDs
docker compose exec garage-node-0 garage node list

# Assign nodes to zones (replace <NODE_ID_*> with the actual node hex IDs)
docker compose exec garage-node-0 garage layout assign \
  --zone dc1 --capacity 1G <NODE_ID_0>

docker compose exec garage-node-0 garage layout assign \
  --zone dc1 --capacity 1G <NODE_ID_1>

docker compose exec garage-node-0 garage layout assign \
  --zone dc1 --capacity 1G <NODE_ID_2>

# Apply the layout
docker compose exec garage-node-0 garage layout apply --version 1
```

### Create a Bucket and Key

```bash
# Create a bucket
docker compose exec garage-node-0 garage bucket create ubag-artifacts

# Create an access key
docker compose exec garage-node-0 garage key create ubag-gateway-key

# Grant the key access to the bucket
docker compose exec garage-node-0 garage bucket allow \
  --read --write --owner ubag-artifacts --key ubag-gateway-key
```

### Gateway Configuration

Point the gateway at node-0 using the standard Garage environment variables:

```bash
export UBAG_GARAGE_ENDPOINT=localhost:3900
export UBAG_GARAGE_ACCESS_KEY=<KEY_ID>
export UBAG_GARAGE_SECRET_KEY=<SECRET_KEY>
export UBAG_GARAGE_BUCKET=ubag-artifacts
export UBAG_GARAGE_USE_SSL=false
```

The `GarageArtifactStore` in `internal/artifacts/garage.go` reads these variables via `NewGarageArtifactStoreFromEnv`.

## Geo-Replication (Multi-Region)

Garage supports two replication strategies:

### 1. Garage Native Replication (within a cluster)

Within a single Garage cluster, set `GARAGE_REPLICATION_FACTOR=3` and assign nodes to different zones. Garage automatically replicates objects across zones. This protects against single-node and single-zone failures.

### 2. Site Replication (between clusters)

For true geo-replication across regions, Garage v0.9+ offers **site replication** (also called cluster-to-cluster replication). This synchronises bucket data between two or more independent Garage clusters in different datacenters.

Configure site replication via the Garage admin API:

```bash
# On cluster-A, add cluster-B as a replication peer
curl -X POST http://localhost:3903/v1/cluster/sync/sites \
  -H "Authorization: Bearer <ADMIN_TOKEN>" \
  -d '{"url": "https://garage-cluster-b.example.com:3903", "secret": "<SHARED_SECRET>"}'
```

Refer to the [Garage site replication docs](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-replication/) for full configuration options.

### 3. Application-Level Replication

The `ReplicatingStore` in `internal/artifacts/replicate.go` provides an application-level fan-out layer. It:

- Writes synchronously to a **home-region** `GarageArtifactStore`.
- Enqueues asynchronous mirror writes to one or more **remote-region** `GarageArtifactStore` instances.
- Mirror failures are logged but never surface to the caller; home-region writes always succeed independently.

This approach is useful when Garage site replication is not yet configured or when you need cross-provider replication (e.g., Garage + MinIO).

```go
home, _ := artifacts.NewGarageArtifactStoreFromEnv(nil) // reads UBAG_GARAGE_* from home region

// Remote region stores are constructed with explicit endpoint values.
remote1, _ := artifacts.NewGarageArtifactStore("garage-eu.example.com:3900", key, secret, bucket, true, nil)
remote2, _ := artifacts.NewGarageArtifactStore("garage-ap.example.com:3900", key, secret, bucket, true, nil)

store := artifacts.NewReplicatingStore(home, []artifacts.ArtifactStore{remote1, remote2}, 128)
store.Start()
defer store.Stop(context.Background())
```

## Production Recommendations

- Replace `GARAGE_RPC_SECRET` with a strong random value (at least 32 bytes of entropy).
- Use TLS for the S3 and admin APIs in production (`UBAG_GARAGE_USE_SSL=true`).
- Assign nodes across physical zones/datacenters for meaningful fault isolation.
- Monitor Garage via its Prometheus `/metrics` endpoint (exposed on the admin port).
- Use the Garage admin API token to restrict management operations.
