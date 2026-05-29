# UBAG Terraform Module

Cloud-agnostic Terraform module that deploys the UBAG gateway by installing the
Helm chart at `deploy/helm/ubag` onto any Kubernetes cluster reachable via a
kubeconfig. Providers are pinned. No cloud-specific resources are used, so the
same module works against EKS, GKE, AKS, k3s, or a local kind/minikube cluster.

## Files

```
deploy/terraform/ubag/
├── versions.tf                # required_version + pinned providers
├── variables.tf               # all inputs (sensitive vars marked)
├── main.tf                    # providers, namespace, optional secret, helm_release
├── outputs.tf                 # release status, secret name, service DNS
├── terraform.tfvars.example   # copy to terraform.tfvars (no real secrets)
└── README.md
```

## Providers

| Provider | Version |
| --- | --- |
| `hashicorp/kubernetes` | `~> 2.31` |
| `hashicorp/helm` | `~> 2.14` |

Terraform `>= 1.6.0`.

## Secrets handling

This module never hardcodes secrets. Two modes:

1. **External Secret (default, recommended):** `manage_secret = false` and
   `existing_secret_name = "ubag-gateway-secrets"`. Create the Secret with
   sealed-secrets, external-secrets, a CSI driver, or `kubectl`. The Helm chart
   consumes it via `secrets.existingSecret`.
2. **Module-managed Secret:** `manage_secret = true`. Supply the sensitive
   values through `TF_VAR_*` environment variables (never committed):
   ```bash
   export TF_VAR_ubag_app_secret=...
   export TF_VAR_ubag_postgres_dsn=...
   export TF_VAR_ubag_minio_access_key=...
   export TF_VAR_ubag_minio_secret_key=...
   export TF_VAR_ubag_webhook_secret=...
   ```

## Usage

```bash
cd deploy/terraform/ubag
cp terraform.tfvars.example terraform.tfvars   # edit (no secrets in the file)

terraform init
terraform fmt -check
terraform validate
terraform plan
terraform apply
```

## Validate offline

```bash
terraform fmt -check deploy/terraform/ubag
terraform -chdir=deploy/terraform/ubag init -backend=false
terraform -chdir=deploy/terraform/ubag validate
```

`terraform init -backend=false` and `validate` run offline (after the one-time
provider download). `plan`/`apply` require a reachable cluster.

## Requires external infra to actually apply

- A reachable Kubernetes cluster + kubeconfig.
- A registry hosting the `ubag/gateway` image.
- External Postgres / NATS / MinIO / Dragonfly endpoints (or chart subcharts).
- Prometheus Operator CRDs if `service_monitor_enabled = true`.
- An ingress controller (+ cert-manager) if `ingress_enabled = true`.

## Validates offline

- `terraform fmt`, `terraform init -backend=false`, `terraform validate`.
