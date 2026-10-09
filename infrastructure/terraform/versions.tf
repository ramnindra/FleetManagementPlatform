terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # terraform-aws-modules/eks//21.x requires AWS provider >= 6.0 in its
      # submodules even though the module's own docs/examples at the time of
      # writing still show "~> 5.0" — found by actually running `terraform
      # init`, not by trusting the published example.
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}
