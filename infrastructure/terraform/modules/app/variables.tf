variable "project_name" { type = string }
variable "environment" { type = string }

variable "vpc_id" { type = string }
variable "private_subnet_ids" { type = list(string) }
variable "eks_node_sg_id" {
  type        = string
  description = "EKS node shared security group — RDS ingress is restricted to traffic from this SG only."
}

variable "db_instance_class" { type = string }
variable "db_allocated_storage_gb" { type = number }
variable "db_engine_version" { type = string }
variable "db_name" { type = string }
variable "db_username" { type = string }
variable "db_multi_az" { type = bool }
variable "db_backup_retention_days" { type = number }

variable "eks_oidc_provider_arn" { type = string }
variable "eks_oidc_provider_url" { type = string }
variable "external_secrets_namespace" { type = string }
variable "external_secrets_service_account" { type = string }
