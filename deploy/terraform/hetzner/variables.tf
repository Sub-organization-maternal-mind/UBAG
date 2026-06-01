variable "hcloud_token" {
  description = "Hetzner Cloud API token (sensitive)."
  type        = string
  sensitive   = true
}

variable "location" {
  description = "Hetzner datacenter location (e.g. nbg1, fsn1, hel1, ash, hil)."
  type        = string
  default     = "nbg1"
}

variable "server_type" {
  description = "Hetzner server type for all nodes (control plane and workers)."
  type        = string
  default     = "cx21"
}

variable "worker_count" {
  description = "Number of k3s worker nodes."
  type        = number
  default     = 2
}

variable "domain" {
  description = "Public domain used for the UBAG ingress hostname."
  type        = string
}

variable "cluster_name" {
  description = "Prefix used for server names and network resources."
  type        = string
  default     = "ubag-k3s"
}

variable "k3s_version" {
  description = "k3s version to install (e.g. v1.30.1+k3s1). Defaults to stable channel."
  type        = string
  default     = "stable"
}

variable "ssh_public_key" {
  description = "SSH public key for server access (the key material, not a file path)."
  type        = string
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
