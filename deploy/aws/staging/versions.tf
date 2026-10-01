terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
  }

  # State is local until an S3 backend with locking exists. It contains no
  # secrets (those live in SSM and are read at boot), but it is still the only
  # record of what was applied: never commit it (ci.yml enforces this).
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "unify-compute"
      Environment = var.environment
      ManagedBy   = "terraform"
      Repository  = "FelixSphere/unify-compute"
    }
  }
}
