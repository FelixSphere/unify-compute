# The provider block, the backend and the default tags live in each
# environment root (deploy/aws/envs/<env>/). A module that configures its own
# provider cannot be given a different region or tags per environment.
terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
  }
}
