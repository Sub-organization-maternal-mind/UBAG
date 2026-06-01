output "cluster_name" {
  description = "GKE cluster name."
  value       = google_container_cluster.ubag.name
}

output "cluster_endpoint" {
  description = "GKE API server endpoint."
  value       = google_container_cluster.ubag.endpoint
}

output "kubeconfig_path" {
  description = "Local path to the generated kubeconfig file."
  value       = local_file.kubeconfig.filename
}

output "ingress_dns" {
  description = "Domain configured for the UBAG ingress."
  value       = var.domain
}

output "cloud_sql_connection_name" {
  description = "Cloud SQL connection name (empty when enable_postgres = false)."
  value       = var.enable_postgres ? google_sql_database_instance.ubag[0].connection_name : ""
}

output "ubag_namespace" {
  description = "Kubernetes namespace for the UBAG release."
  value       = module.ubag.namespace
}

output "ubag_release_status" {
  description = "Helm release status."
  value       = module.ubag.release_status
}
