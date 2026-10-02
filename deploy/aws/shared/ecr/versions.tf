terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project    = "unify-compute"
      ManagedBy  = "terraform"
      Repository = "FelixSphere/unify-compute"
    }
  }
}
