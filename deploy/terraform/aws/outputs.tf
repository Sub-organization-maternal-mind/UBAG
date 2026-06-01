output "cluster_name" {
  description = "EKS cluster name."
  value       = aws_eks_cluster.ubag.name
}

output "cluster_endpoint" {
  description = "EKS API server endpoint."
  value       = aws_eks_cluster.ubag.endpoint
}

output "kubeconfig_path" {
  description = "Local path to the generated kubeconfig file."
  value       = local_file.kubeconfig.filename
}

output "ingress_dns" {
  description = "Domain configured for the UBAG ingress."
  value       = var.domain
}

output "rds_endpoint" {
  description = "RDS PostgreSQL endpoint (empty when enable_postgres = false)."
  value       = var.enable_postgres ? aws_db_instance.ubag[0].address : ""
}

output "s3_bucket_name" {
  description = "S3 bucket name (empty when enable_s3 = false)."
  value       = var.enable_s3 ? aws_s3_bucket.ubag[0].bucket : ""
}

output "ubag_namespace" {
  description = "Kubernetes namespace for the UBAG release."
  value       = module.ubag.namespace
}

output "ubag_release_status" {
  description = "Helm release status."
  value       = module.ubag.release_status
}
