# GeoDNS / Anycast Edge — UBAG Multi-Region

This directory contains documentation and Terraform for routing global traffic
to the nearest healthy UBAG region using latency-based DNS (GeoDNS) or anycast.

---

## 1. GeoDNS — AWS Route53 or Cloudflare

### AWS Route53 (latency-based routing)

Route53 latency records direct each client to the lowest-latency region based
on the origin of the DNS query. The `route53.tf` file in this directory creates:

- One `aws_route53_record` per region with `latency_routing_policy`.
- One `aws_route53_health_check` per region that polls `/v1/ready` on the
  regional Caddy edge every 30 seconds.

When `/v1/ready` returns HTTP 503 (or times out), Route53 marks the region
unhealthy and stops routing new traffic to it within one TTL interval (TTL=30s).

### Cloudflare (latency / proximity routing)

Configure Cloudflare Load Balancing with:

- **Origin groups**: one pool per region, each pointing to the Caddy edge IP.
- **Health check**: HTTPS to `/v1/ready`, 30-second interval, threshold 2.
- **Steering policy**: "Latency" (round-trip time based) or "Geo" (region map).

Failover behaviour is identical: a pool that fails its health check is removed
from rotation automatically.

---

## 2. Anycast

For providers that support anycast (Cloudflare, Fastly, or a BGP-anycast VPS):

1. Announce the same anycast IP prefix from both region-a and region-b.
2. BGP routing naturally directs each client to the closest PoP.
3. Caddy in each region terminates TLS and proxies to the local gateway pod.
4. Health checks still apply: the anycast node must respond 200 on `/v1/ready`
   or the upstream load-balancer withdraws it from the pool.

---

## 3. Health Check Endpoint

Both DNS providers check `/v1/ready` on the regional Caddy edge:

```
GET https://gateway-{a,b}.ubag.example.com/v1/ready
```

- Returns `HTTP 200` with body `{"status":"ok"}` when the region is healthy.
- Returns `HTTP 503` when the gateway process is unavailable or the database
  connection pool is exhausted.
- Caddy forwards the request to the gateway on port 8080; no auth required.

```
# Caddy snippet (deploy/multi-region/caddy/Caddyfile.global)
handle /v1/ready {
  reverse_proxy gateway:8080
}
```

---

## 4. Failover Behaviour

| Provider | Failure detection | Cutover time |
|---|---|---|
| Route53 | 3 consecutive failures × 30 s interval | ~90–120 s + TTL (30 s) |
| Cloudflare | 2 consecutive failures × 30 s interval | ~60–90 s |
| Anycast / BGP | Immediate on route withdrawal | Seconds (BGP convergence) |

Recovery is automatic: once `/v1/ready` returns 200 for the required number of
consecutive checks, the provider reinstates the region.

---

## 5. Terraform (Route53)

See `route53.tf` for a complete, deployable sample. Variables required:

| Variable | Description |
|---|---|
| `hosted_zone_id` | Route53 hosted zone for `ubag.example.com` |
| `region_a_ip` | Public IP of the region-a Caddy edge |
| `region_b_ip` | Public IP of the region-b Caddy edge |

Apply with:

```bash
cd deploy/multi-region/geodns
terraform init
terraform plan -var hosted_zone_id=ZXXXXXXXXXXXXX \
               -var region_a_ip=1.2.3.4 \
               -var region_b_ip=5.6.7.8
terraform apply
```

---

## References

- Route53 latency routing: https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-policy-latency.html
- Cloudflare Load Balancing: https://developers.cloudflare.com/load-balancing/
- NATS supercluster config: `deploy/multi-region/nats/`
- pgactive config: `deploy/multi-region/postgres/pgactive.md`
