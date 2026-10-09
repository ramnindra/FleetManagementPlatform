locals {
  name = "${var.project_name}-${var.environment}"
  azs  = slice(data.aws_availability_zones.available.names, 0, var.availability_zone_count)

  # /24s carved out of var.vpc_cidr: one private + one public per AZ.
  private_subnets = [for i in range(var.availability_zone_count) : cidrsubnet(var.vpc_cidr, 8, i)]
  public_subnets  = [for i in range(var.availability_zone_count) : cidrsubnet(var.vpc_cidr, 8, i + 100)]
}

data "aws_availability_zones" "available" {
  state = "available"
}

# Looked up, not created here: this repository is created once by
# infrastructure/terraform/bootstrap, shared across every environment's
# `terraform apply` of this root module. See modules/app/main.tf's comment
# on why ECR does not live in the per-environment app module.
data "aws_ecr_repository" "controller" {
  name = var.project_name
}

# --- Networking -------------------------------------------------------------
# Public subnets: only the ALB/NLB and NAT gateways live here. Private
# subnets: EKS nodes and RDS — nothing in them is ever internet-routable,
# satisfying "do not expose PostgreSQL directly to the public internet."
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"

  name = local.name
  cidr = var.vpc_cidr

  azs             = local.azs
  private_subnets = local.private_subnets
  public_subnets  = local.public_subnets

  enable_nat_gateway   = true
  single_nat_gateway   = var.environment == "dev" # one shared NAT in dev to save cost; one per AZ in staging/prod for HA
  enable_dns_hostnames = true
  enable_dns_support   = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1" # required for the AWS Load Balancer Controller to pick these for internet-facing ALBs
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }

  tags = {
    Project     = var.project_name
    Environment = var.environment
  }
}

# --- EKS ---------------------------------------------------------------------
module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.0"

  name               = local.name
  kubernetes_version = var.eks_cluster_version

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets # nodes live in private subnets only

  endpoint_public_access                   = var.eks_endpoint_public_access
  endpoint_public_access_cidrs             = var.eks_public_access_cidrs
  enable_cluster_creator_admin_permissions = true

  eks_managed_node_groups = {
    default = {
      instance_types = var.eks_node_instance_types
      min_size       = var.eks_node_min_size
      max_size       = var.eks_node_max_size
      desired_size   = var.eks_node_desired_size
    }
  }

  tags = {
    Project     = var.project_name
    Environment = var.environment
  }
}

# --- App-specific infra (RDS, ECR, Secrets Manager, External-Secrets IRSA) --
module "app" {
  source = "./modules/app"

  project_name = var.project_name
  environment  = var.environment

  vpc_id             = module.vpc.vpc_id
  private_subnet_ids = module.vpc.private_subnets
  eks_node_sg_id     = module.eks.node_security_group_id

  db_instance_class        = var.db_instance_class
  db_allocated_storage_gb  = var.db_allocated_storage_gb
  db_engine_version        = var.db_engine_version
  db_name                  = var.db_name
  db_username              = var.db_username
  db_multi_az              = var.db_multi_az
  db_backup_retention_days = var.db_backup_retention_days

  eks_oidc_provider_arn            = module.eks.oidc_provider_arn
  eks_oidc_provider_url            = module.eks.cluster_oidc_issuer_url
  external_secrets_namespace       = var.external_secrets_namespace
  external_secrets_service_account = var.external_secrets_service_account
}
