provider "google" {
  project = var.project
  region  = var.region
}

# ---------------------------------------------------------------------------
# GKE cluster (VPC-native, autopilot disabled for node pool control)
# ---------------------------------------------------------------------------
resource "google_container_cluster" "ubag" {
  name     = var.cluster_name
  location = var.region

  # Remove the default node pool after creation; use a managed node pool.
  remove_default_node_pool = true
  initial_node_count       = 1

  min_master_version = var.kubernetes_version

  networking_mode = "VPC_NATIVE"

  ip_allocation_policy {}

  release_channel {
    channel = "REGULAR"
  }

  workload_identity_config {
    workload_pool = "${var.project}.svc.id.goog"
  }
}

# ---------------------------------------------------------------------------
# Managed node pool
# ---------------------------------------------------------------------------
resource "google_container_node_pool" "ubag_workers" {
  name       = "${var.cluster_name}-workers"
  location   = var.region
  cluster    = google_container_cluster.ubag.name
  node_count = var.node_count

  node_config {
    machine_type = var.machine_type
    disk_size_gb = 50
    disk_type    = "pd-standard"

    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform",
    ]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# ---------------------------------------------------------------------------
# kubeconfig file (written locally for Helm provider)
# ---------------------------------------------------------------------------
locals {
  kubeconfig_content = yamlencode({
    apiVersion = "v1"
    kind       = "Config"
    clusters = [{
      name = var.cluster_name
      cluster = {
        server                     = "https://${google_container_cluster.ubag.endpoint}"
        certificate-authority-data = google_container_cluster.ubag.master_auth[0].cluster_ca_certificate
      }
    }]
    users = [{
      name = "gke-user"
      user = {
        exec = {
          apiVersion = "client.authentication.k8s.io/v1beta1"
          command    = "gke-gcloud-auth-plugin"
          installHint = "Install gke-gcloud-auth-plugin: https://cloud.google.com/blog/products/containers-kubernetes/kubectl-auth-changes-in-gke"
          provideClusterInfo = true
        }
      }
    }]
    contexts = [{
      name = var.cluster_name
      context = {
        cluster = var.cluster_name
        user    = "gke-user"
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

  depends_on = [google_container_node_pool.ubag_workers]
}

# ---------------------------------------------------------------------------
# Optional: Cloud SQL PostgreSQL
# ---------------------------------------------------------------------------
resource "google_sql_database_instance" "ubag" {
  count = var.enable_postgres ? 1 : 0

  name             = "${var.cluster_name}-postgres"
  database_version = "POSTGRES_15"
  region           = var.region

  settings {
    tier = var.postgres_tier

    backup_configuration {
      enabled = true
    }

    ip_configuration {
      ipv4_enabled = true
    }
  }

  deletion_protection = false
}

resource "google_sql_user" "ubag" {
  count    = var.enable_postgres ? 1 : 0
  instance = google_sql_database_instance.ubag[0].name
  name     = "ubag"
  password = var.postgres_password
}

resource "google_sql_database" "ubag" {
  count    = var.enable_postgres ? 1 : 0
  instance = google_sql_database_instance.ubag[0].name
  name     = "ubag"
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
