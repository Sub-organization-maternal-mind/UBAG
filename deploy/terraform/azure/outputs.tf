output "cluster_name" {
  description = "AKS cluster name."
  value       = azurerm_kubernetes_cluster.ubag.name
}

output "cluster_endpoint" {
  description = "AKS API server endpoint."
  value       = azurerm_kubernetes_cluster.ubag.kube_config[0].host
}

output "kubeconfig_path" {
  description = "Local path to the generated kubeconfig file."
  value       = local_file.kubeconfig.filename
}

output "ingress_dns" {
  description = "Domain configured for the UBAG ingress."
  value       = var.domain
}

output "postgres_fqdn" {
  description = "PostgreSQL Flexible Server FQDN (empty when enable_postgres = false)."
  value       = var.enable_postgres ? azurerm_postgresql_flexible_server.ubag[0].fqdn : ""
}

output "resource_group_name" {
  description = "Azure resource group name."
  value       = azurerm_resource_group.ubag.name
}

output "ubag_namespace" {
  description = "Kubernetes namespace for the UBAG release."
  value       = module.ubag.namespace
}

output "ubag_release_status" {
  description = "Helm release status."
  value       = module.ubag.release_status
}
