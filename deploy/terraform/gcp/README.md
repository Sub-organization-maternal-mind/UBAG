# UBAG — GCP / GKE Terraform Module

Provisions a Google Kubernetes Engine cluster with a managed node pool and
deploys UBAG via the shared `../ubag` Helm module. Optionally creates a
Cloud SQL PostgreSQL instance.

## Quick start

```bash
# 1. Authenticate with GCP
gcloud auth application-default login
# or use a service account key:
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/sa-key.json"

# 2. Supply secrets via environment variables (never hard-code)
export TF_VAR_ubag_app_secret="$(openssl rand -hex 32)"
export TF_VAR_ubag_postgres_dsn="postgres://ubag:password@host:5432/ubag"
export TF_VAR_ubag_webhook_secret="$(openssl rand -hex 24)"

# 3. Copy the example vars file and edit
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars

# 4. Init, plan, apply
terraform init
terraform plan -out=tfplan
terraform apply tfplan
```

## Prerequisites

- `gke-gcloud-auth-plugin` installed for kubectl access:
  `gcloud components install gke-gcloud-auth-plugin`

## Variables

| Name | Description | Default |
|---|---|---|
| `project` | GCP project ID | _(required)_ |
| `region` | GCP region | `us-central1` |
| `zone` | GCP zone | `us-central1-a` |
| `cluster_name` | GKE cluster name | `ubag-gke` |
| `node_count` | Nodes per zone | `3` |
| `machine_type` | GCE machine type | `e2-standard-2` |
| `domain` | Ingress hostname | _(required)_ |
| `kubernetes_version` | Min Kubernetes version | `1.30` |
| `enable_postgres` | Provision Cloud SQL Postgres | `false` |
| `postgres_tier` | Cloud SQL machine tier | `db-f1-micro` |
| `postgres_password` | Cloud SQL password _(sensitive)_ | `""` |
| `ubag_version` | Gateway image tag | `latest` |
| `ubag_app_secret` | UBAG_APP_SECRET _(sensitive)_ | `""` |
| `ubag_postgres_dsn` | UBAG_POSTGRES_DSN _(sensitive)_ | `""` |
| `ubag_minio_access_key` | UBAG_MINIO_ACCESS_KEY _(sensitive)_ | `""` |
| `ubag_minio_secret_key` | UBAG_MINIO_SECRET_KEY _(sensitive)_ | `""` |
| `ubag_webhook_secret` | UBAG_WEBHOOK_SECRET _(sensitive)_ | `""` |

## Outputs

| Name | Description |
|---|---|
| `cluster_name` | GKE cluster name |
| `cluster_endpoint` | GKE API server endpoint |
| `kubeconfig_path` | Local path to generated kubeconfig |
| `ingress_dns` | Domain configured for UBAG ingress |
| `cloud_sql_connection_name` | Cloud SQL connection name (empty when disabled) |
| `ubag_namespace` | Kubernetes namespace |
| `ubag_release_status` | Helm release status |

## Teardown

```bash
terraform destroy
```
