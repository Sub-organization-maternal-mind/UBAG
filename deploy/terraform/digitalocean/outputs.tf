output "cluster_name" {
  description = "DOKS cluster name."
  value       = digitalocean_kubernetes_cluster.ubag.name
}

output "cluster_id" {
  description = "DOKS cluster ID."
  value       = digitalocean_kubernetes_cluster.ubag.id
}

output "cluster_endpoint" {
  description = "DOKS API server endpoint."
  value       = digitalocean_kubernetes_cluster.ubag.endpoint
}

output "kubeconfig_path" {
  description = "Local path to the generated kubeconfig file."
  value       = local_file.kubeconfig.filename
}

output "ingress_ip" {
  description = "IPv4 address to point DNS A-records at (auto-provisioned load balancer IP)."
  # The LB IP is assigned by DigitalOcean after an ingress Service of type LoadBalancer is created.
  # Retrieve it with: kubectl get svc -n ingress-nginx
  value       = "See: kubectl get svc -n ingress-nginx ingress-nginx-controller -o jsonpath='{.status.loadBalancer.ingress[0].ip}'"
}

output "ingress_dns" {
  description = "Domain configured for the UBAG ingress."
  value       = var.domain
}

output "postgres_uri" {
  description = "Managed PostgreSQL connection URI (empty when enable_postgres = false, sensitive)."
  value       = var.enable_postgres ? digitalocean_database_cluster.ubag_postgres[0].uri : ""
  sensitive   = true
}

output "ubag_namespace" {
  description = "Kubernetes namespace for the UBAG release."
  value       = module.ubag.namespace
}

output "ubag_release_status" {
  description = "Helm release status."
  value       = module.ubag.release_status
}
