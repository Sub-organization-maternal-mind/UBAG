# UBAG Helm Chart

Helm chart for the **standard** (Kubernetes) and **enterprise** (multi-region
HA) UBAG deployment profiles. It deploys the Go gateway image built from
`deploy/small/gateway.Dockerfile` with production-grade defaults: HPA,
PodDisruptionBudget, NetworkPolicy, hardened SecurityContexts, and an optional
Prometheus Operator `ServiceMonitor`.

Backing services (Postgres, Dragonfly, NATS, MinIO) are consumed as **external
managed services by default**. Optional in-cluster subcharts can be enabled via
the commented `dependencies` block in `Chart.yaml` plus the `<service>.deploy`
toggles in `values.yaml`.

## Layout

```
deploy/helm/ubag/
├── Chart.yaml                 # chart metadata + (commented) subchart deps
├── values.yaml                # defaults (secrets blank)
├── values-production.yaml     # production overrides (HPA, ingress, TLS, SM)
├── .helmignore
├── README.md
└── templates/
    ├── _helpers.tpl
    ├── configmap.yaml          # non-secret UBAG_* env
    ├── secret.yaml             # secret UBAG_* env (skipped if existingSecret)
    ├── serviceaccount.yaml
    ├── deployment.yaml         # probes /v1/health, /v1/ready
    ├── service.yaml
    ├── ingress.yaml
    ├── hpa.yaml
    ├── pdb.yaml
    ├── networkpolicy.yaml
    ├── servicemonitor.yaml
    └── NOTES.txt
```

## Configuration model

- **Non-secret config** → `values.config.*` → rendered into a `ConfigMap` and
  injected with `envFrom`. Keys mirror `deploy/small/env.example` (`UBAG_*`).
- **Secrets** → `values.secrets.*` → rendered into a `Secret` and injected with
  `envFrom`. Defaults are empty. Set `secrets.existingSecret` to consume an
  externally-managed Secret (sealed-secrets, external-secrets, CSI) — the chart
  then creates no Secret of its own.

Secret keys: `UBAG_APP_SECRET`, `UBAG_POSTGRES_DSN`, `UBAG_MINIO_ACCESS_KEY`,
`UBAG_MINIO_SECRET_KEY`, `UBAG_WEBHOOK_SECRET`.

## Validate offline

```bash
helm lint deploy/helm/ubag
helm lint deploy/helm/ubag -f deploy/helm/ubag/values-production.yaml

# Render without a cluster:
helm template ubag deploy/helm/ubag
helm template ubag deploy/helm/ubag -f deploy/helm/ubag/values-production.yaml
```

`helm lint` and `helm template` pass fully offline because no hard subchart
dependencies are declared. Enabling in-cluster subcharts requires
`helm dependency update deploy/helm/ubag` first (needs network access).

## Install (requires a cluster + image)

```bash
# Create the secret out-of-band (example; use a real secret manager):
kubectl create secret generic ubag-gateway-secrets \
  --from-literal=UBAG_APP_SECRET="$APP_SECRET" \
  --from-literal=UBAG_POSTGRES_DSN="$PG_DSN" \
  --from-literal=UBAG_MINIO_ACCESS_KEY="$MINIO_KEY" \
  --from-literal=UBAG_MINIO_SECRET_KEY="$MINIO_SECRET" \
  --from-literal=UBAG_WEBHOOK_SECRET="$WH_SECRET"

helm upgrade --install ubag deploy/helm/ubag \
  -f deploy/helm/ubag/values-production.yaml \
  --set image.tag=1.0.0 \
  --set secrets.existingSecret=ubag-gateway-secrets \
  --namespace ubag --create-namespace
```

## Requires external infra to actually run

- A Kubernetes cluster and a registry hosting the `ubag/gateway` image.
- External Postgres, NATS, MinIO/S3, and Dragonfly endpoints (or enable the
  optional subcharts).
- `ServiceMonitor` requires the Prometheus Operator CRDs installed.
- `Ingress`/TLS requires an ingress controller and (for the production values)
  cert-manager.

## Validates offline

- `helm lint` and `helm template` (manifest correctness, value wiring).
