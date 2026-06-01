variable "region" {
  description = "AWS region in which to create resources."
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "Name of the EKS cluster."
  type        = string
  default     = "ubag-eks"
}

variable "node_count" {
  description = "Desired number of worker nodes."
  type        = number
  default     = 3
}

variable "node_size" {
  description = "EC2 instance type for worker nodes."
  type        = string
  default     = "t3.medium"
}

variable "domain" {
  description = "Public domain used for the UBAG ingress hostname (e.g. api.example.com)."
  type        = string
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version."
  type        = string
  default     = "1.30"
}

# ---------------------------------------------------------------------------
# Optional: managed RDS Postgres
# ---------------------------------------------------------------------------
variable "enable_postgres" {
  description = "When true, provision an RDS PostgreSQL instance."
  type        = bool
  default     = false
}

variable "postgres_instance_class" {
  description = "RDS instance class (used when enable_postgres = true)."
  type        = string
  default     = "db.t3.micro"
}

variable "postgres_password" {
  description = "Master password for the RDS instance (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

# ---------------------------------------------------------------------------
# Optional: S3 bucket for object storage
# ---------------------------------------------------------------------------
variable "enable_s3" {
  description = "When true, provision an S3 bucket for UBAG object storage."
  type        = bool
  default     = false
}

variable "s3_bucket_name" {
  description = "Name of the S3 bucket (globally unique; used when enable_s3 = true)."
  type        = string
  default     = ""
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
  description = "UBAG_APP_SECRET passed to the Helm release (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_postgres_dsn" {
  description = "UBAG_POSTGRES_DSN passed to the Helm release (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_minio_access_key" {
  description = "UBAG_MINIO_ACCESS_KEY passed to the Helm release (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_minio_secret_key" {
  description = "UBAG_MINIO_SECRET_KEY passed to the Helm release (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ubag_webhook_secret" {
  description = "UBAG_WEBHOOK_SECRET passed to the Helm release (sensitive)."
  type        = string
  default     = ""
  sensitive   = true
}
