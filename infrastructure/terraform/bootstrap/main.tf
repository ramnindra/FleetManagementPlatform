# Bootstrap: creates the S3 bucket + DynamoDB lock table that the main
# infrastructure/terraform root's S3 backend needs to exist BEFORE it can
# even run `terraform init`. This is the standard chicken-and-egg answer for
# Terraform remote state: something has to create the state backend itself,
# and that something cannot use the backend it is creating.
#
# Run this once per AWS account/region, with its own LOCAL state (never
# migrate this one to S3) — see docs/deployment.md Phase 6 for the exact
# commands. It is small and changes rarely, so local state here is a
# reasonable, explicitly-chosen trade-off, not an oversight.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

variable "aws_region" {
  type        = string
  description = "AWS region to create the state backend resources in."
  default     = "us-east-1"
}

variable "project_name" {
  type        = string
  description = "Used to name the bucket/table so they don't collide with other projects in the same account."
  default     = "device-controller"
}

variable "ecr_image_tag_mutability" {
  type        = string
  default     = "IMMUTABLE"
  description = "IMMUTABLE enforces 'never deploy a mutable :latest tag to production' at the registry level, not just by CI convention."
}

variable "github_org" {
  type        = string
  description = "GitHub org/user that owns the repo — scopes the OIDC trust policy so only workflows running in THIS repo can assume the role."
  default     = "REPLACE_WITH_GITHUB_ORG"
}

variable "github_repo" {
  type    = string
  default = "cloud-device-controller"
}

variable "create_github_oidc_provider" {
  type        = bool
  default     = true
  description = "false if this AWS account already has a GitHub Actions OIDC provider (an account can only have one per issuer URL) — set github_oidc_provider_arn instead."
}

variable "github_oidc_provider_arn" {
  type    = string
  default = ""
}

resource "aws_s3_bucket" "tfstate" {
  bucket = "${var.project_name}-tfstate-${data.aws_caller_identity.current.account_id}"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "tfstate" {
  bucket                  = aws_s3_bucket.tfstate.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "tflock" {
  name         = "${var.project_name}-tfstate-lock"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }
}

# --- ECR ---------------------------------------------------------------
# Created once here, not per environment: dev/staging/prod are separate
# `terraform apply` runs against infrastructure/terraform's root module, and
# CI builds exactly one image per merge to main and promotes that same
# immutable digest through all three — there is only ever one repository.
resource "aws_ecr_repository" "controller" {
  name                 = var.project_name
  image_tag_mutability = var.ecr_image_tag_mutability

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "controller" {
  repository = aws_ecr_repository.controller.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after 14 days"
      selection = {
        tagStatus   = "untagged"
        countType   = "sinceImagePushed"
        countUnit   = "days"
        countNumber = 14
      }
      action = { type = "expire" }
    }]
  })
}

# --- GitHub Actions OIDC federation -----------------------------------
# No long-lived AWS access keys in CI: GitHub's OIDC token server issues a
# short-lived token per workflow run, which this trust relationship lets
# GitHub exchange for temporary AWS credentials via sts:AssumeRoleWithWebIdentity.
data "tls_certificate" "github_oidc" {
  url = "https://token.actions.githubusercontent.com/.well-known/openid-configuration"
}

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_github_oidc_provider ? 1 : 0

  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github_oidc.certificates[0].sha1_fingerprint]
}

locals {
  github_oidc_provider_arn = var.create_github_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.github_oidc_provider_arn
}

# Scoped narrowly: only workflows running on `main`, or under a GitHub
# Environment named "production" (the approval gate for promote-prod.yml),
# in this exact repo — not "any workflow in any repo in this GitHub org."
data "aws_iam_policy_document" "github_actions_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.github_oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:${var.github_org}/${var.github_repo}:ref:refs/heads/main",
        "repo:${var.github_org}/${var.github_repo}:environment:production",
      ]
    }
  }
}

resource "aws_iam_role" "github_actions" {
  name               = "${var.project_name}-github-actions"
  assume_role_policy = data.aws_iam_policy_document.github_actions_trust.json
}

# Least privilege: push/pull on this one repo, nothing else. No ecr:*,
# no access to any other service.
data "aws_iam_policy_document" "github_actions_ecr" {
  statement {
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"] # this specific action does not support resource-level restriction
  }
  statement {
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:GetDownloadUrlForLayer",
      "ecr:BatchGetImage",
      "ecr:PutImage",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
    ]
    resources = [aws_ecr_repository.controller.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_ecr" {
  name   = "${var.project_name}-github-actions-ecr"
  role   = aws_iam_role.github_actions.id
  policy = data.aws_iam_policy_document.github_actions_ecr.json
}

data "aws_caller_identity" "current" {}

output "bucket_name" {
  value = aws_s3_bucket.tfstate.id
}

output "ecr_repository_url" {
  value = aws_ecr_repository.controller.repository_url
}

output "github_actions_role_arn" {
  description = "Set as the AWS_ROLE_ARN repo/environment variable GitHub Actions workflows assume via OIDC — see docs/cicd.md."
  value       = aws_iam_role.github_actions.arn
}

output "dynamodb_table_name" {
  value = aws_dynamodb_table.tflock.name
}

output "backend_hcl_snippet" {
  description = "Paste these values into environments/<env>/backend.hcl"
  value       = <<-EOT
    bucket         = "${aws_s3_bucket.tfstate.id}"
    dynamodb_table = "${aws_dynamodb_table.tflock.name}"
    region         = "${var.aws_region}"
    encrypt        = true
  EOT
}
