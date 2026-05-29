locals {
  # Resolve a local chart path relative to this module unless a repo is given.
  use_repo   = var.chart_repository != null
  chart_ref  = local.use_repo ? var.chart_name : "${path.module}/${var.chart_path}"
  secret_ref = var.manage_secret ? kubernetes_secret_v1.ubag[0].metadata[0].name : var.existing_secret_name
}

provider "kubernetes" {
  config_path    = var.kube_config_path
  config_context = var.kube_config_context != "" ? var.kube_config_context : null
}

provider "helm" {
  kubernetes {
    config_path    = var.kube_config_path
    config_context = var.kube_config_context != "" ? var.kube_config_context : null
  }
}

resource "kubernetes_namespace_v1" "ubag" {
  count = var.create_namespace ? 1 : 0

  metadata {
    name = var.namespace
    labels = {
      "app.kubernetes.io/part-of" = "ubag"
    }
  }
}

# Optional: create the UBAG gateway Secret from sensitive variables.
# Disabled by default; prefer an externally-managed Secret (manage_secret=false).
resource "kubernetes_secret_v1" "ubag" {
  count = var.manage_secret ? 1 : 0

  metadata {
    name      = var.secret_name
    namespace = var.namespace
    labels = {
      "app.kubernetes.io/part-of"    = "ubag"
      "app.kubernetes.io/managed-by" = "terraform"
    }
  }

  type = "Opaque"

  data = {
    UBAG_APP_SECRET       = var.ubag_app_secret
    UBAG_POSTGRES_DSN     = var.ubag_postgres_dsn
    UBAG_MINIO_ACCESS_KEY = var.ubag_minio_access_key
    UBAG_MINIO_SECRET_KEY = var.ubag_minio_secret_key
    UBAG_WEBHOOK_SECRET   = var.ubag_webhook_secret
  }

  depends_on = [kubernetes_namespace_v1.ubag]
}

resource "helm_release" "ubag" {
  name      = var.release_name
  namespace = var.namespace

  chart      = local.chart_ref
  repository = local.use_repo ? var.chart_repository : null
  version    = local.use_repo ? var.chart_version : null

  create_namespace = false
  atomic           = true
  cleanup_on_fail  = true
  wait             = true
  timeout          = 600

  # Merge any additional values files (e.g. values-production.yaml).
  values = [for f in var.values_files : file(f)]

  set {
    name  = "image.repository"
    value = var.image_repository
  }

  dynamic "set" {
    for_each = var.image_tag != "" ? [var.image_tag] : []
    content {
      name  = "image.tag"
      value = set.value
    }
  }

  set {
    name  = "replicaCount"
    value = tostring(var.replica_count)
  }

  set {
    name  = "autoscaling.enabled"
    value = tostring(var.autoscaling_enabled)
  }

  set {
    name  = "ingress.enabled"
    value = tostring(var.ingress_enabled)
  }

  dynamic "set" {
    for_each = var.ingress_enabled ? [1] : []
    content {
      name  = "ingress.className"
      value = var.ingress_class_name
    }
  }

  dynamic "set" {
    for_each = var.ingress_enabled ? [1] : []
    content {
      name  = "ingress.hosts[0].host"
      value = var.ingress_host
    }
  }

  set {
    name  = "serviceMonitor.enabled"
    value = tostring(var.service_monitor_enabled)
  }

  # Point the chart at the secret (created here or externally-managed).
  set {
    name  = "secrets.existingSecret"
    value = local.secret_ref
  }

  dynamic "set" {
    for_each = var.extra_set
    content {
      name  = set.key
      value = set.value
    }
  }

  depends_on = [
    kubernetes_namespace_v1.ubag,
    kubernetes_secret_v1.ubag,
  ]
}
