output "configure_kubectl" {
  description = "Run this after apply to point kubectl at the new cluster."
  value       = "aws eks update-kubeconfig --region ${var.aws_region} --name ${module.eks.cluster_name}"
}

output "cluster_name" {
  value = module.eks.cluster_name
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

output "vpc_id" {
  value = module.vpc.vpc_id
}

output "ecr_repository_url" {
  value = data.aws_ecr_repository.controller.repository_url
}

output "rds_endpoint" {
  value       = module.app.db_endpoint
  description = "host:port — does not include credentials. The full CONTROLLER_DATABASE_URL is stored in Secrets Manager, never in Terraform output/state in plaintext beyond what RDS itself requires."
}

output "secrets_manager_prefix" {
  value       = module.app.secrets_prefix
  description = "Base path under which this environment's 4 app secrets live (database-url, mqtt-client-password, mqtt-webhook-shared-secret, admin-api-key) — matches controller.externalSecrets.remoteRefPrefix in kubernetes/helm/device-controller/values-*.yaml."
}

output "external_secrets_irsa_role_arn" {
  value       = module.app.external_secrets_role_arn
  description = "Annotate the External Secrets Operator's ServiceAccount with eks.amazonaws.com/role-arn = this value."
}
