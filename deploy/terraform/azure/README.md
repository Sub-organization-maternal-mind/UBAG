# UBAG — Azure / AKS Terraform Module

Provisions an Azure Kubernetes Service cluster and deploys UBAG via the shared
`../ubag` Helm module. Optionally creates an Azure PostgreSQL Flexible Server.

## Quick start

```bash
# 1. Authenticate with Azure
az login
# or use a service principal:
export ARM_CLIENT_ID="..."
export ARM_CLIENT_SECRET="..."
export ARM_SUBSCRIPTION_ID="..."
export ARM_TENANT_ID="..."

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
| `location` | Azure region | `East US` |
| `resource_group` | Resource group name | `ubag-rg` |
| `cluster_name` | AKS cluster name | `ubag-aks` |
| `node_count` | Node count in default pool | `3` |
| `vm_size` | Azure VM size | `Standard_D2s_v3` |
| `domain` | Ingress hostname | _(required)_ |
| `kubernetes_version` | Kubernetes version | `1.30` |
| `enable_postgres` | Provision PostgreSQL Flexible Server | `false` |
| `postgres_sku_name` | PostgreSQL SKU | `B_Standard_B1ms` |
| `postgres_admin_login` | PostgreSQL admin login | `ubagadmin` |
| `postgres_admin_password` | PostgreSQL admin password _(sensitive)_ | `""` |
| `ubag_version` | Gateway image tag | `latest` |
| `ubag_app_secret` | UBAG_APP_SECRET _(sensitive)_ | `""` |
| `ubag_postgres_dsn` | UBAG_POSTGRES_DSN _(sensitive)_ | `""` |
| `ubag_minio_access_key` | UBAG_MINIO_ACCESS_KEY _(sensitive)_ | `""` |
| `ubag_minio_secret_key` | UBAG_MINIO_SECRET_KEY _(sensitive)_ | `""` |
| `ubag_webhook_secret` | UBAG_WEBHOOK_SECRET _(sensitive)_ | `""` |

## Outputs

| Name | Description |
|---|---|
| `cluster_name` | AKS cluster name |
| `cluster_endpoint` | AKS API server URL |
| `kubeconfig_path` | Local path to generated kubeconfig |
| `ingress_dns` | Domain configured for UBAG ingress |
| `postgres_fqdn` | PostgreSQL FQDN (empty when disabled) |
| `resource_group_name` | Azure resource group name |
| `ubag_namespace` | Kubernetes namespace |
| `ubag_release_status` | Helm release status |

## Teardown

```bash
terraform destroy
```
