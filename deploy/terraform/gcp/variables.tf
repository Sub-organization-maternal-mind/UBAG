variable "project" {
  description = "GCP project ID in which to create resources."
  type        = string
}

variable "region" {
  description = "GCP region (e.g. us-central1)."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "GCP zone for zonal resources."
  type        = string
  default     = "us-central1-a"
}

variable "cluster_name" {
  description = "Name of the GKE cluster."
  type        = string
  default     = "ubag-gke"
}

variable "node_count" {
  description = "Number of nodes per zone in the default node pool."
  type        = number
  default     = 3
}

variable "machine_type" {
  description = "GCE machine type for worker nodes."
  type        = string
  default     = "e2-standard-2"
}

variable "domain" {
  description = "Public domain used for the UBAG ingress hostname."
  type        = string
}

variable "kubernetes_version" {
  description = "Minimum Kubernetes version for the GKE cluster (e.g. 1.30)."
  type        = string
  default     = "1.30"
}

# ---------------------------------------------------------------------------
# Optional: Cloud SQL PostgreSQL
# ---------------------------------------------------------------------------
variable "enable_postgres" {
  description = "When true, provision a Cloud SQL PostgreSQL instance."
  type        = bool
  default     = false
}

variable "postgres_tier" {
  description = "Cloud SQL machine tier (used when enable_postgres = true)."
  type        = string
  default     = "db-f1-micro"
}

variable "postgres_password" {
  description = "Cloud SQL postgres user password (sensitive)."
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
