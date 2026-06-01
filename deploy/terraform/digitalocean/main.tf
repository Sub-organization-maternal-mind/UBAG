provider "digitalocean" {
  token = var.do_token
}

# ---------------------------------------------------------------------------
# Data: latest DOKS version matching the requested minor version
# ---------------------------------------------------------------------------
data "digitalocean_kubernetes_versions" "available" {
  version_prefix = "${var.kubernetes_version}."
}

# ---------------------------------------------------------------------------
# DOKS cluster
# ---------------------------------------------------------------------------
resource "digitalocean_kubernetes_cluster" "ubag" {
  name    = var.cluster_name
  region  = var.region
  version = data.digitalocean_kubernetes_versions.available.latest_version

  node_pool {
    name       = "${var.cluster_name}-workers"
    size       = var.node_size
    node_count = var.node_count

    labels = {
      project = "ubag"
    }
  }

  tags = ["ubag", "kubernetes"]
}

# ---------------------------------------------------------------------------
# kubeconfig file (written locally for Helm provider)
# ---------------------------------------------------------------------------
locals {
  kubeconfig_path = "${path.module}/.kubeconfig"
}

resource "local_file" "kubeconfig" {
  content         = digitalocean_kubernetes_cluster.ubag.kube_config[0].raw_config
  filename        = local.kubeconfig_path
  file_permission = "0600"
}

# ---------------------------------------------------------------------------
# Optional: DigitalOcean Managed PostgreSQL
# ---------------------------------------------------------------------------
resource "digitalocean_database_cluster" "ubag_postgres" {
  count = var.enable_postgres ? 1 : 0

  name       = "${var.cluster_name}-postgres"
  engine     = "pg"
  version    = "15"
  size       = var.postgres_size
  region     = var.region
  node_count = var.postgres_node_count

  tags = ["ubag"]
}

resource "digitalocean_database_db" "ubag" {
  count      = var.enable_postgres ? 1 : 0
  cluster_id = digitalocean_database_cluster.ubag_postgres[0].id
  name       = "ubag"
}

resource "digitalocean_database_firewall" "ubag_postgres" {
  count      = var.enable_postgres ? 1 : 0
  cluster_id = digitalocean_database_cluster.ubag_postgres[0].id

  rule {
    type  = "k8s"
    value = digitalocean_kubernetes_cluster.ubag.id
  }
}

# ---------------------------------------------------------------------------
# UBAG Helm module
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
