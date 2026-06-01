provider "azurerm" {
  features {}
}

# ---------------------------------------------------------------------------
# Resource group
# ---------------------------------------------------------------------------
resource "azurerm_resource_group" "ubag" {
  name     = var.resource_group
  location = var.location
}

# ---------------------------------------------------------------------------
# AKS cluster
# ---------------------------------------------------------------------------
resource "azurerm_kubernetes_cluster" "ubag" {
  name                = var.cluster_name
  location            = azurerm_resource_group.ubag.location
  resource_group_name = azurerm_resource_group.ubag.name
  dns_prefix          = var.cluster_name
  kubernetes_version  = var.kubernetes_version

  default_node_pool {
    name       = "default"
    node_count = var.node_count
    vm_size    = var.vm_size
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin = "azure"
    network_policy = "calico"
  }

  tags = {
    environment = "production"
    project     = "ubag"
  }
}

# ---------------------------------------------------------------------------
# kubeconfig file (written locally for Helm provider)
# ---------------------------------------------------------------------------
locals {
  kubeconfig_path = "${path.module}/.kubeconfig"
}

resource "local_file" "kubeconfig" {
  content         = azurerm_kubernetes_cluster.ubag.kube_config_raw
  filename        = local.kubeconfig_path
  file_permission = "0600"
}

# ---------------------------------------------------------------------------
# Optional: Azure PostgreSQL Flexible Server
# ---------------------------------------------------------------------------
resource "azurerm_postgresql_flexible_server" "ubag" {
  count = var.enable_postgres ? 1 : 0

  name                   = "${var.cluster_name}-postgres"
  resource_group_name    = azurerm_resource_group.ubag.name
  location               = azurerm_resource_group.ubag.location
  version                = "15"
  administrator_login    = var.postgres_admin_login
  administrator_password = var.postgres_admin_password
  sku_name               = var.postgres_sku_name
  storage_mb             = 32768

  backup_retention_days = 7
}

resource "azurerm_postgresql_flexible_server_database" "ubag" {
  count  = var.enable_postgres ? 1 : 0
  name   = "ubag"
  server_id = azurerm_postgresql_flexible_server.ubag[0].id
  charset   = "UTF8"
  collation = "en_US.utf8"
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
