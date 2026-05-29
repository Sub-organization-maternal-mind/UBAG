variable "kube_config_path" {
  description = "Path to the kubeconfig file used to reach the target cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_config_context" {
  description = "kubeconfig context to use. Empty uses the current-context."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Kubernetes namespace for the UBAG release."
  type        = string
  default     = "ubag"
}

variable "create_namespace" {
  description = "Whether to create the namespace."
  type        = bool
  default     = true
}

variable "release_name" {
  description = "Helm release name."
  type        = string
  default     = "ubag"
}

variable "chart_path" {
  description = "Path to the UBAG Helm chart. Relative paths are resolved against this module."
  type        = string
  default     = "../../helm/ubag"
}

variable "chart_version" {
  description = "Chart version to install when chart_repository is set. Ignored for local chart_path."
  type        = string
  default     = null
}

variable "chart_repository" {
  description = "Optional Helm repository URL. When set, chart_name is pulled from it instead of chart_path."
  type        = string
  default     = null
}

variable "chart_name" {
  description = "Chart name when installing from chart_repository."
  type        = string
  default     = "ubag"
}

variable "image_repository" {
  description = "Gateway image repository."
  type        = string
  default     = "ubag/gateway"
}

variable "image_tag" {
  description = "Gateway image tag. Empty uses the chart appVersion."
  type        = string
  default     = ""
}

variable "replica_count" {
  description = "Replica count when autoscaling is disabled."
  type        = number
  default     = 3
}

variable "autoscaling_enabled" {
  description = "Enable the HorizontalPodAutoscaler."
  type        = bool
  default     = true
}

variable "ingress_enabled" {
  description = "Enable the gateway Ingress."
  type        = bool
  default     = false
}

variable "ingress_host" {
  description = "Ingress hostname (when ingress_enabled)."
  type        = string
  default     = "ubag.example.com"
}

variable "ingress_class_name" {
  description = "Ingress class name."
  type        = string
  default     = "nginx"
}

variable "service_monitor_enabled" {
  description = "Create a Prometheus Operator ServiceMonitor (CRDs must exist)."
  type        = bool
  default     = false
}

variable "values_files" {
  description = "Additional Helm values files (paths) merged in order, e.g. values-production.yaml."
  type        = list(string)
  default     = []
}

variable "extra_set" {
  description = "Additional Helm --set values as a map of dot-path => string."
  type        = map(string)
  default     = {}
}

# ---------------------------------------------------------------------------
# Secrets. Provide via TF_VAR_* environment variables, a secret backend, or a
# tfvars file that is NOT committed. Defaults are empty and MUST be supplied.
# When manage_secret = false the chart consumes an externally-managed Secret
# referenced by existing_secret_name.
# ---------------------------------------------------------------------------
variable "manage_secret" {
  description = "If true, this module creates the UBAG gateway Secret from the sensitive vars. If false, set existing_secret_name."
  type        = bool
  default     = false
}

variable "existing_secret_name" {
  description = "Name of an externally-managed Secret with the UBAG_* secret keys (used when manage_secret = false)."
  type        = string
  default     = "ubag-gateway-secrets"
}

variable "secret_name" {
  description = "Name of the Secret created when manage_secret = true."
  type        = string
  default     = "ubag-gateway-secrets"
}

variable "ubag_app_secret" {
  description = "UBAG_APP_SECRET (sensitive). Required when manage_secret = true."
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
