provider "hcloud" {
  token = var.hcloud_token
}

# ---------------------------------------------------------------------------
# SSH key
# ---------------------------------------------------------------------------
resource "hcloud_ssh_key" "ubag" {
  name       = "${var.cluster_name}-key"
  public_key = var.ssh_public_key
}

# ---------------------------------------------------------------------------
# Private network for cluster internal traffic
# ---------------------------------------------------------------------------
resource "hcloud_network" "ubag" {
  name     = "${var.cluster_name}-net"
  ip_range = "10.0.0.0/8"
}

resource "hcloud_network_subnet" "ubag" {
  type         = "cloud"
  network_id   = hcloud_network.ubag.id
  network_zone = "eu-central"
  ip_range     = "10.0.1.0/24"
}

# ---------------------------------------------------------------------------
# k3s token (shared secret between control plane and workers)
# ---------------------------------------------------------------------------
resource "tls_private_key" "k3s_token_rng" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

locals {
  # Derive a stable token from the private key material (no sensitive resource needed)
  k3s_token = substr(sha256(tls_private_key.k3s_token_rng.private_key_pem), 0, 48)

  control_plane_userdata = <<-EOT
    #!/bin/bash
    set -euo pipefail

    # Install k3s control plane
    K3S_VERSION="${var.k3s_version}"
    if [ "$K3S_VERSION" = "stable" ]; then
      INSTALL_FLAG=""
    else
      INSTALL_FLAG="INSTALL_K3S_VERSION=$K3S_VERSION"
    fi

    curl -sfL https://get.k3s.io | \
      K3S_TOKEN="${local.k3s_token}" \
      INSTALL_K3S_EXEC="server --disable traefik --cluster-init --tls-san $(curl -s http://169.254.169.254/hetzner/v1/metadata/public-ipv4)" \
      $${INSTALL_FLAG} sh -

    # Wait for node to be ready
    until kubectl get nodes 2>/dev/null | grep -q " Ready"; do sleep 5; done

    # Install NGINX ingress controller
    kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.10.1/deploy/static/provider/baremetal/deploy.yaml

    # Copy kubeconfig for external access
    mkdir -p /tmp/kubeconfig
    K3S_IP=$(curl -s http://169.254.169.254/hetzner/v1/metadata/public-ipv4)
    sed "s/127.0.0.1/$K3S_IP/g" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig/config
    chmod 600 /tmp/kubeconfig/config
  EOT

  worker_userdata = <<-EOT
    #!/bin/bash
    set -euo pipefail

    K3S_VERSION="${var.k3s_version}"
    if [ "$K3S_VERSION" = "stable" ]; then
      INSTALL_FLAG=""
    else
      INSTALL_FLAG="INSTALL_K3S_VERSION=$K3S_VERSION"
    fi

    # Wait for control plane to be ready
    until curl -sk https://${hcloud_server.control_plane.ipv4_address}:6443/ping 2>/dev/null | grep -q ok; do sleep 10; done

    curl -sfL https://get.k3s.io | \
      K3S_URL="https://${hcloud_server.control_plane.ipv4_address}:6443" \
      K3S_TOKEN="${local.k3s_token}" \
      $${INSTALL_FLAG} sh -
  EOT
}

# ---------------------------------------------------------------------------
# Control plane server
# ---------------------------------------------------------------------------
resource "hcloud_server" "control_plane" {
  name        = "${var.cluster_name}-cp"
  server_type = var.server_type
  image       = "ubuntu-22.04"
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.ubag.id]
  user_data   = local.control_plane_userdata

  network {
    network_id = hcloud_network.ubag.id
    ip         = "10.0.1.1"
  }

  depends_on = [hcloud_network_subnet.ubag]
}

# ---------------------------------------------------------------------------
# Worker nodes
# ---------------------------------------------------------------------------
resource "hcloud_server" "workers" {
  count = var.worker_count

  name        = "${var.cluster_name}-worker-${count.index}"
  server_type = var.server_type
  image       = "ubuntu-22.04"
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.ubag.id]
  user_data   = local.worker_userdata

  network {
    network_id = hcloud_network.ubag.id
    ip         = "10.0.1.${count.index + 10}"
  }

  depends_on = [
    hcloud_server.control_plane,
    hcloud_network_subnet.ubag,
  ]
}

# ---------------------------------------------------------------------------
# kubeconfig file — fetched from control plane via SSH null_resource
# We write a placeholder kubeconfig pointing at the control plane IP;
# the actual cluster CA / token are established after cloud-init runs.
# For production use, retrieve kubeconfig via SSH after apply completes.
# ---------------------------------------------------------------------------
locals {
  kubeconfig_content = yamlencode({
    apiVersion = "v1"
    kind       = "Config"
    clusters = [{
      name = var.cluster_name
      cluster = {
        server                     = "https://${hcloud_server.control_plane.ipv4_address}:6443"
        insecure-skip-tls-verify   = true
      }
    }]
    users = [{
      name = "k3s-admin"
      user = {
        # Token is retrieved after cloud-init completes:
        # ssh root@<cp_ip> cat /etc/rancher/k3s/k3s.yaml
        token = "REPLACE_WITH_TOKEN_FROM_K3S_YAML"
      }
    }]
    contexts = [{
      name = var.cluster_name
      context = {
        cluster = var.cluster_name
        user    = "k3s-admin"
      }
    }]
    current-context = var.cluster_name
  })

  kubeconfig_path = "${path.module}/.kubeconfig"
}

resource "local_file" "kubeconfig" {
  content         = local.kubeconfig_content
  filename        = local.kubeconfig_path
  file_permission = "0600"

  depends_on = [hcloud_server.control_plane]
}

# ---------------------------------------------------------------------------
# UBAG Helm module
# NOTE: Helm deployment will only succeed after k3s cloud-init is complete
# (typically 3–5 minutes after `terraform apply`). If Helm times out, re-run
# `terraform apply` once the cluster is healthy.
# ---------------------------------------------------------------------------
module "ubag" {
  source = "../ubag"

  kube_config_path    = local_file.kubeconfig.filename
  kube_config_context = var.cluster_name
  release_name        = "ubag"
  chart_path          = "../../helm/ubag"
  namespace           = "ubag"
  create_namespace    = true

  ingress_enabled    = true
  ingress_host       = var.domain
  ingress_class_name = "nginx"

  image_tag = var.ubag_version

  manage_secret         = true
  ubag_app_secret       = var.ubag_app_secret
  ubag_postgres_dsn     = var.ubag_postgres_dsn
  ubag_minio_access_key = var.ubag_minio_access_key
  ubag_minio_secret_key = var.ubag_minio_secret_key
  ubag_webhook_secret   = var.ubag_webhook_secret

  depends_on = [local_file.kubeconfig]
}
