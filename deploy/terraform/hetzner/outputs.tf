output "control_plane_ip" {
  description = "Public IPv4 address of the k3s control plane server."
  value       = hcloud_server.control_plane.ipv4_address
}

output "worker_ips" {
  description = "Public IPv4 addresses of k3s worker nodes."
  value       = hcloud_server.workers[*].ipv4_address
}

output "kubeconfig_path" {
  description = "Local path to the kubeconfig file. Update with credentials after cloud-init completes."
  value       = local_file.kubeconfig.filename
}

output "ingress_ip" {
  description = "Control plane IP to point DNS A-records at (until a load balancer is added)."
  value       = hcloud_server.control_plane.ipv4_address
}

output "ingress_dns" {
  description = "Domain configured for the UBAG ingress."
  value       = var.domain
}

output "kubeconfig_fetch_command" {
  description = "SSH command to fetch the final kubeconfig from the control plane."
  value       = "ssh root@${hcloud_server.control_plane.ipv4_address} cat /tmp/kubeconfig/config > .kubeconfig"
}

output "ubag_namespace" {
  description = "Kubernetes namespace for the UBAG release."
  value       = module.ubag.namespace
}

output "ubag_release_status" {
  description = "Helm release status."
  value       = module.ubag.release_status
}
