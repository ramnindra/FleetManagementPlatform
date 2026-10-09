variable "project_name" {
  type        = string
  default     = "device-controller"
  description = "Prefix applied to resource names and tags."
}

variable "environment" {
  type        = string
  description = "dev | staging | prod — drives sizing defaults and tagging. Matches a file under environments/."

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be one of: dev, staging, prod."
  }
}

variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "vpc_cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "availability_zone_count" {
  type        = number
  default     = 2
  description = "Number of AZs to spread public/private subnets across. 2 is the minimum for RDS/EKS HA; 3 for prod-grade."
}

variable "eks_cluster_version" {
  type    = string
  default = "1.33"
}

variable "eks_node_instance_types" {
  type    = list(string)
  default = ["t3.medium"]
}

variable "eks_node_min_size" {
  type    = number
  default = 2
}

variable "eks_node_max_size" {
  type    = number
  default = 4
}

variable "eks_node_desired_size" {
  type    = number
  default = 2
}

variable "eks_endpoint_public_access" {
  type        = bool
  default     = true
  description = "true = cluster API reachable from the internet, scoped down by eks_public_access_cidrs below. This project has no bastion/VPN, so flipping this to false entirely would make the cluster unmanageable from outside the VPC — restricting the CIDR list is the realistic hardening step, not disabling public access outright."
}

variable "eks_public_access_cidrs" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = "CIDRs allowed to reach the public EKS API endpoint. The default is wide open and fine only for a disposable dev cluster — staging/prod MUST override this to your actual office/VPN egress CIDR(s) before applying. See environments/prod/terraform.tfvars."
}

variable "db_instance_class" {
  type    = string
  default = "db.t3.micro"
}

variable "db_allocated_storage_gb" {
  type    = number
  default = 20
}

variable "db_engine_version" {
  type    = string
  default = "16"
}

variable "db_name" {
  type    = string
  default = "controller"
}

variable "db_username" {
  type    = string
  default = "controller"
}

variable "db_multi_az" {
  type        = bool
  default     = false
  description = "Multi-AZ failover — doubles RDS cost. true for staging/prod, false for dev. See environments/*/terraform.tfvars."
}

variable "db_backup_retention_days" {
  type    = number
  default = 7
}

variable "external_secrets_namespace" {
  type        = string
  default     = "external-secrets"
  description = <<-EOT
    Namespace the External Secrets Operator itself (the cluster-wide platform
    component that reads this role, not our controller app) runs in. It is a
    prerequisite installed separately from this Terraform/Helm chart — see
    docs/deployment.md Phase 6 — so this must match whatever namespace/
    ServiceAccount name that installation actually used.
  EOT
}

variable "external_secrets_service_account" {
  type        = string
  default     = "external-secrets"
  description = "ServiceAccount name of the External Secrets Operator itself — see external_secrets_namespace."
}
