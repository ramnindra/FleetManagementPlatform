# Fully partial backend config: Terraform backend blocks cannot reference
# variables, only literals, so bucket/key/table/region all come from
# environments/<env>/backend.hcl at init time:
#   terraform init -backend-config=environments/dev/backend.hcl
# `key` is set per environment too (e.g. "device-controller/dev/terraform.tfstate")
# so dev/staging/prod never share one state file. The bucket/table values come
# from infrastructure/terraform/bootstrap's outputs — see docs/deployment.md
# Phase 6 for the one-time bootstrap sequence.
terraform {
  backend "s3" {}
}
