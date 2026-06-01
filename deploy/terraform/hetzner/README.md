# UBAG — Hetzner Cloud / k3s Terraform Module

Provisions a k3s Kubernetes cluster on Hetzner Cloud servers using cloud-init
and deploys UBAG via the shared `../ubag` Helm module.

> Hetzner does not offer a managed Kubernetes service. This module bootstraps
> k3s on a control-plane server plus `worker_count` worker nodes, all in a
> private Hetzner network.

## Architecture

```
Internet ──► Control plane (cx21) ──► Worker 0 (cx21)
                                  ──► Worker 1 (cx21)
                  ↕ private network (10.0.1.0/24)
```

The control plane runs `k3s server` with the NGINX ingress controller.
Workers join via `k3s agent`.

## Quick start

```bash
# 1. Set your Hetzner API token (never hard-code it)
export TF_VAR_hcloud_token="hetzner-api-token-here"

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

# 5. Fetch the real kubeconfig after cloud-init completes (~5 min)
ssh root@$(terraform output -raw control_plane_ip) \
  cat /tmp/kubeconfig/config > .kubeconfig
export KUBECONFIG=$PWD/.kubeconfig
kubectl get nodes
```

## Notes on cloud-init timing

The Helm module is invoked during `terraform apply` but k3s cloud-init takes
3–5 minutes to finish. If Helm times out, re-run `terraform apply` once
`kubectl get nodes` shows all nodes `Ready`.

## Variables

| Name | Description | Default |
|---|---|---|
| `hcloud_token` | Hetzner API token _(sensitive)_ | _(required)_ |
| `location` | Datacenter location | `nbg1` |
| `server_type` | Hetzner server type | `cx21` |
| `worker_count` | Number of worker nodes | `2` |
| `domain` | Ingress hostname | _(required)_ |
| `cluster_name` | Name prefix for resources | `ubag-k3s` |
| `k3s_version` | k3s version | `stable` |
| `ssh_public_key` | SSH public key material | _(required)_ |
| `ubag_version` | Gateway image tag | `latest` |
| `ubag_app_secret` | UBAG_APP_SECRET _(sensitive)_ | `""` |
| `ubag_postgres_dsn` | UBAG_POSTGRES_DSN _(sensitive)_ | `""` |
| `ubag_minio_access_key` | UBAG_MINIO_ACCESS_KEY _(sensitive)_ | `""` |
| `ubag_minio_secret_key` | UBAG_MINIO_SECRET_KEY _(sensitive)_ | `""` |
| `ubag_webhook_secret` | UBAG_WEBHOOK_SECRET _(sensitive)_ | `""` |

## Outputs

| Name | Description |
|---|---|
| `control_plane_ip` | Control plane public IP |
| `worker_ips` | Worker node public IPs |
| `kubeconfig_path` | Local kubeconfig path (placeholder — see step 5) |
| `ingress_ip` | IP to use for DNS A-records |
| `ingress_dns` | Domain configured for UBAG ingress |
| `kubeconfig_fetch_command` | SSH command to fetch real kubeconfig |
| `ubag_namespace` | Kubernetes namespace |
| `ubag_release_status` | Helm release status |

## Teardown

```bash
terraform destroy
```
