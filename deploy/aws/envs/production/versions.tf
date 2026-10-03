terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
  }

  # State lives in S3 with native locking; see backend.tf. `use_lockfile` is
  # why the floor is 1.10 and not 1.9 — on 1.9.x this configuration parses as
  # an unknown argument and `init` fails, which is a worse place to learn it
  # than here.
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "unify-compute"
      Environment = "production"
      ManagedBy   = "terraform"
      Repository  = "FelixSphere/unify-compute"
    }
  }
}
