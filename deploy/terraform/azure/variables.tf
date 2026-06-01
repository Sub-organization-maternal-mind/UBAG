variable "location" {
  description = "Azure region (e.g. East US)."
  type        = string
  default     = "East US"
}

variable "resource_group" {
  description = "Name of the Azure resource group to create."
  type        = string
  default     = "ubag-rg"
}

variable "cluster_name" {
  description = "Name of the AKS cluster."
  type        = string
  default     = "ubag-aks"
}

variable "node_count" {
  description = "Number of nodes in the default node pool."
  type        = number
  default     = 3
}

variable "vm_size" {
  description = "Azure VM size for AKS worker nodes."
  type        = string
  default     = "Standard_D2s_v3"
}

variable "domain" {
  description = "Public domain used for the UBAG ingress hostname."
  type        = string
}

variable "kubernetes_version" {
  description = "Kubernetes version for the AKS cluster (e.g. 1.30)."
  type        = string
  default     = "1.30"
}

# ---------------------------------------------------------------------------
# Optional: Azure Database for PostgreSQL Flexible Server
# ---------------------------------------------------------------------------
variable "enable_postgres" {
  description = "When true, provision an Azure PostgreSQL Flexible Server."
  type        = bool
  default     = false
}

variable "postgres_sku_name" {
  description = "PostgreSQL Flexible Server SKU (used when enable_postgres = true)."
  type        = string
  default     = "B_Standard_B1ms"
}

variable "postgres_admin_login" {
  description = "Administrator login for the PostgreSQL server."
  type        = string
  default     = "ubagadmin"
}

variable "postgres_admin_password" {
  description = "Administrator password for the PostgreSQL server (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

# ---------------------------------------------------------------------------
# UBAG Helm module passthrough
# ---------------------------------------------------------------------------
variable "ubag_version" {
  description = "Gateway Docker image tag deployed via Helm."
  type        = string
  default     = "latest"
}

variable "ubag_app_secret" {
  description = "UBAG_APP_SECRET (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_postgres_dsn" {
  description = "UBAG_POSTGRES_DSN (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_minio_access_key" {
  description = "UBAG_MINIO_ACCESS_KEY (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_minio_secret_key" {
  description = "UBAG_MINIO_SECRET_KEY (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_webhook_secret" {
  description = "UBAG_WEBHOOK_SECRET (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}
