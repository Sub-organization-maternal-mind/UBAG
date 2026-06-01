# DR-relevant shared variables for UBAG deployments.
# Include this module in cloud-specific modules for consistent DR config.

variable "backup_bucket" {
  description = "Object storage bucket for UBAG backups (Phase 7 backup engine target)"
  type        = string
  default     = ""
}

variable "wal_archive_bucket" {
  description = "Object storage bucket/path for Postgres WAL archiving (used by Phase 7 restore)"
  type        = string
  default     = ""
}

variable "wal_archive_prefix" {
  description = "Key prefix within wal_archive_bucket for WAL segments"
  type        = string
  default     = "wal-archive"
}

variable "replica_count" {
  description = "Number of UBAG gateway replicas (used for HA sizing)"
  type        = number
  default     = 2
}

variable "postgres_replica_count" {
  description = "Number of Postgres read replicas for HA"
  type        = number
  default     = 1
}

variable "rto_minutes" {
  description = "Recovery Time Objective in minutes (informational, used in DR runbook)"
  type        = number
  default     = 30
}

variable "rpo_minutes" {
  description = "Recovery Point Objective in minutes (informational, used in DR runbook)"
  type        = number
  default     = 5
}
