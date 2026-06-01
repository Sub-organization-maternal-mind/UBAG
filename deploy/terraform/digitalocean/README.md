# UBAG — DigitalOcean / DOKS Terraform Module

Provisions a DigitalOcean Kubernetes Service (DOKS) cluster and deploys UBAG
via the shared `../ubag` Helm module. Optionally creates a DigitalOcean Managed
PostgreSQL cluster.

## Quick start

```bash
# 1. Set your DigitalOcean token (never hard-code it)
export TF_VAR_do_token="your-do-token-here"

# 2. Supply UBAG secrets
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

# 5. Get ingress IP after nginx ingress controller is deployed
kubectl get svc -n ingress-nginx ingress-nginx-controller \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

## Variables

| Name | Description | Default |
|---|---|---|
| `do_token` | DigitalOcean API token _(sensitive)_ | _(required)_ |
| `region` | DigitalOcean region slug | `nyc3` |
| `cluster_name` | DOKS cluster name | `ubag-doks` |
| `node_count` | Number of worker nodes | `3` |
| `node_size` | Droplet size slug | `s-2vcpu-4gb` |
| `domain` | Ingress hostname | _(required)_ |
| `kubernetes_version` | Kubernetes minor version prefix | `1.30` |
| `enable_postgres` | Provision Managed PostgreSQL | `false` |
| `postgres_size` | Database node size | `db-s-1vcpu-1gb` |
| `postgres_node_count` | Database node count | `1` |
| `ubag_version` | Gateway image tag | `latest` |
| `ubag_app_secret` | UBAG_APP_SECRET _(sensitive)_ | `""` |
| `ubag_postgres_dsn` | UBAG_POSTGRES_DSN _(sensitive)_ | `""` |
| `ubag_minio_access_key` | UBAG_MINIO_ACCESS_KEY _(sensitive)_ | `""` |
| `ubag_minio_secret_key` | UBAG_MINIO_SECRET_KEY _(sensitive)_ | `""` |
| `ubag_webhook_secret` | UBAG_WEBHOOK_SECRET _(sensitive)_ | `""` |

## Outputs

| Name | Description |
|---|---|
| `cluster_name` | DOKS cluster name |
| `cluster_id` | DOKS cluster ID |
| `cluster_endpoint` | DOKS API server endpoint |
| `kubeconfig_path` | Local path to generated kubeconfig |
| `ingress_ip` | Instructions to retrieve LB IP |
| `ingress_dns` | Domain configured for UBAG ingress |
| `postgres_uri` | Managed PostgreSQL URI _(sensitive, empty when disabled)_ |
| `ubag_namespace` | Kubernetes namespace |
| `ubag_release_status` | Helm release status |

## Teardown

```bash
terraform destroy
```
