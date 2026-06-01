variable "do_token" {
  description = "DigitalOcean personal access token (sensitive)."
  type        = string
  sensitive   = true
}

variable "region" {
  description = "DigitalOcean region slug (e.g. nyc3, fra1, sgp1)."
  type        = string
  default     = "nyc3"
}

variable "cluster_name" {
  description = "Name of the DOKS cluster."
  type        = string
  default     = "ubag-doks"
}

variable "node_count" {
  description = "Number of nodes in the default node pool."
  type        = number
  default     = 3
}

variable "node_size" {
  description = "DigitalOcean Droplet size slug for worker nodes."
  type        = string
  default     = "s-2vcpu-4gb"
}

variable "domain" {
  description = "Public domain used for the UBAG ingress hostname."
  type        = string
}

variable "kubernetes_version" {
  description = "DOKS Kubernetes version prefix (e.g. 1.30). Latest patch is used."
  type        = string
  default     = "1.30"
}

# ---------------------------------------------------------------------------
# Optional: DigitalOcean Managed PostgreSQL
# ---------------------------------------------------------------------------
variable "enable_postgres" {
  description = "When true, provision a DigitalOcean Managed PostgreSQL cluster."
  type        = bool
  default     = false
}

variable "postgres_size" {
  description = "Database cluster node size (used when enable_postgres = true)."
  type        = string
  default     = "db-s-1vcpu-1gb"
}

variable "postgres_node_count" {
  description = "Number of database nodes (1 = single node; 2+ = primary+standby)."
  type        = number
  default     = 1
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
