locals {
  name           = "${var.project_name}-${var.environment}"
  secrets_prefix = "device-controller/${var.environment}"
}

# --- RDS PostgreSQL -----------------------------------------------------
# Private subnets only, security group restricted to the EKS node SG —
# never reachable from the public internet. See docs/architecture.md.
resource "aws_db_subnet_group" "this" {
  name       = local.name
  subnet_ids = var.private_subnet_ids
}

resource "aws_security_group" "rds" {
  name        = "${local.name}-rds"
  description = "Allow Postgres only from EKS nodes"
  vpc_id      = var.vpc_id

  ingress {
    description     = "Postgres from EKS nodes"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [var.eks_node_sg_id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "random_password" "db_master" {
  length  = 32
  special = false # fed directly into a postgresql:// URL; avoiding special chars sidesteps URL-encoding edge cases entirely
}

resource "aws_db_instance" "this" {
  identifier     = local.name
  engine         = "postgres"
  engine_version = var.db_engine_version

  instance_class    = var.db_instance_class
  allocated_storage = var.db_allocated_storage_gb
  storage_encrypted = true

  db_name  = var.db_name
  username = var.db_username
  password = random_password.db_master.result

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  publicly_accessible    = false

  multi_az                = var.db_multi_az
  backup_retention_period = var.db_backup_retention_days
  skip_final_snapshot     = var.environment == "dev" # staging/prod always take a final snapshot on destroy
  deletion_protection     = var.environment != "dev"

  tags = {
    Project     = var.project_name
    Environment = var.environment
  }
}

# ECR deliberately lives in infrastructure/terraform/bootstrap, not here.
# This module is instantiated once PER ENVIRONMENT (dev/staging/prod are
# separate `terraform apply` runs with separate state) — an ECR repository
# created here would either collide across environments (same AWS account)
# or fragment into 3 repos, breaking "build once in CI, promote the same
# immutable digest through dev -> staging -> prod" into "rebuild per
# environment." ECR is an account/region-wide resource that needs to exist
# exactly once, same category as the state bucket bootstrap already owns.

# --- Secrets Manager -------------------------------------------------------
# Four secrets, matching exactly what kubernetes/helm/device-controller's
# ExternalSecret (controller-externalsecret.yaml) reads via
# {remoteRefPrefix}/{suffix} — keep these two in sync by hand, since Helm and
# Terraform are deliberately separate tools here (see docs/architecture.md
# "Helm and Terraform" distinction).
resource "random_password" "mqtt_client_password" {
  length  = 32
  special = false
}
resource "random_password" "mqtt_webhook_shared_secret" {
  length  = 32
  special = false
}
resource "random_password" "admin_api_key" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "database_url" {
  name = "${local.secrets_prefix}/database-url"
}
resource "aws_secretsmanager_secret_version" "database_url" {
  secret_id     = aws_secretsmanager_secret.database_url.id
  secret_string = "postgresql://${var.db_username}:${random_password.db_master.result}@${aws_db_instance.this.address}:5432/${var.db_name}"
}

resource "aws_secretsmanager_secret" "mqtt_client_password" {
  name = "${local.secrets_prefix}/mqtt-client-password"
}
resource "aws_secretsmanager_secret_version" "mqtt_client_password" {
  secret_id     = aws_secretsmanager_secret.mqtt_client_password.id
  secret_string = random_password.mqtt_client_password.result
}

resource "aws_secretsmanager_secret" "mqtt_webhook_shared_secret" {
  name = "${local.secrets_prefix}/mqtt-webhook-shared-secret"
}
resource "aws_secretsmanager_secret_version" "mqtt_webhook_shared_secret" {
  secret_id     = aws_secretsmanager_secret.mqtt_webhook_shared_secret.id
  secret_string = random_password.mqtt_webhook_shared_secret.result
}

resource "aws_secretsmanager_secret" "admin_api_key" {
  name = "${local.secrets_prefix}/admin-api-key"
}
resource "aws_secretsmanager_secret_version" "admin_api_key" {
  secret_id     = aws_secretsmanager_secret.admin_api_key.id
  secret_string = random_password.admin_api_key.result
}

# --- IRSA role for the External Secrets Operator --------------------------
# Trusts only the specific namespace:serviceaccount the Operator runs as
# (not "any pod in the cluster"), and the attached policy grants read access
# to only these 4 secret ARNs — not secretsmanager:* on *. The Operator
# itself (the controller that assumes this role) is a platform prerequisite
# installed separately — see docs/deployment.md Phase 6.
data "aws_iam_policy_document" "external_secrets_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [var.eks_oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${replace(var.eks_oidc_provider_url, "https://", "")}:sub"
      values   = ["system:serviceaccount:${var.external_secrets_namespace}:${var.external_secrets_service_account}"]
    }

    condition {
      test     = "StringEquals"
      variable = "${replace(var.eks_oidc_provider_url, "https://", "")}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "external_secrets" {
  name               = "${local.name}-external-secrets"
  assume_role_policy = data.aws_iam_policy_document.external_secrets_trust.json
}

data "aws_iam_policy_document" "external_secrets_permissions" {
  statement {
    effect = "Allow"
    actions = [
      "secretsmanager:GetSecretValue",
      "secretsmanager:DescribeSecret",
    ]
    resources = [
      aws_secretsmanager_secret.database_url.arn,
      aws_secretsmanager_secret.mqtt_client_password.arn,
      aws_secretsmanager_secret.mqtt_webhook_shared_secret.arn,
      aws_secretsmanager_secret.admin_api_key.arn,
    ]
  }
}

resource "aws_iam_role_policy" "external_secrets" {
  name   = "${local.name}-external-secrets"
  role   = aws_iam_role.external_secrets.id
  policy = data.aws_iam_policy_document.external_secrets_permissions.json
}
