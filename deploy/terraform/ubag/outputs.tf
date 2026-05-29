output "namespace" {
  description = "Namespace the UBAG release was deployed into."
  value       = var.namespace
}

output "release_name" {
  description = "Helm release name."
  value       = helm_release.ubag.name
}

output "release_status" {
  description = "Helm release status."
  value       = helm_release.ubag.status
}

output "chart_version" {
  description = "Resolved chart version."
  value       = helm_release.ubag.version
}

output "secret_name" {
  description = "Name of the Secret the gateway consumes for UBAG_* secret env."
  value       = local.secret_ref
}

output "service_dns" {
  description = "In-cluster DNS name of the gateway Service."
  value       = "${var.release_name}.${var.namespace}.svc.cluster.local"
}
