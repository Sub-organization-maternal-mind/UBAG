# UBAG — AWS / EKS Terraform Module

Provisions an Amazon EKS cluster with a managed node group and deploys UBAG
via the shared `../ubag` Helm module. Optionally creates an RDS PostgreSQL
instance and an S3 bucket.

## Quick start

```bash
# 1. Authenticate with AWS
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_REGION="us-east-1"

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

## Variables

| Name | Description | Default |
|---|---|---|
| `region` | AWS region | `us-east-1` |
| `cluster_name` | EKS cluster name | `ubag-eks` |
| `node_count` | Desired worker node count | `3` |
| `node_size` | EC2 instance type | `t3.medium` |
| `domain` | Ingress hostname | _(required)_ |
| `kubernetes_version` | EKS Kubernetes version | `1.30` |
| `enable_postgres` | Provision RDS PostgreSQL | `false` |
| `postgres_instance_class` | RDS instance class | `db.t3.micro` |
| `postgres_password` | RDS master password _(sensitive)_ | `""` |
| `enable_s3` | Provision S3 bucket | `false` |
| `s3_bucket_name` | S3 bucket name (globally unique) | `""` |
| `ubag_version` | Gateway image tag | `latest` |
| `ubag_app_secret` | UBAG_APP_SECRET _(sensitive)_ | `""` |
| `ubag_postgres_dsn` | UBAG_POSTGRES_DSN _(sensitive)_ | `""` |
| `ubag_minio_access_key` | UBAG_MINIO_ACCESS_KEY _(sensitive)_ | `""` |
| `ubag_minio_secret_key` | UBAG_MINIO_SECRET_KEY _(sensitive)_ | `""` |
| `ubag_webhook_secret` | UBAG_WEBHOOK_SECRET _(sensitive)_ | `""` |

## Outputs

| Name | Description |
|---|---|
| `cluster_name` | EKS cluster name |
| `cluster_endpoint` | EKS API server URL |
| `kubeconfig_path` | Local path to generated kubeconfig |
| `ingress_dns` | Domain configured for UBAG ingress |
| `rds_endpoint` | RDS endpoint (empty when disabled) |
| `s3_bucket_name` | S3 bucket name (empty when disabled) |
| `ubag_namespace` | Kubernetes namespace |
| `ubag_release_status` | Helm release status |

## Teardown

```bash
terraform destroy
```

> **Note:** The RDS instance has `skip_final_snapshot = true` for ease of teardown
> in non-production environments. Set it to `false` and configure a snapshot
> identifier for production use.
