# Providers, backend and default tags come from the environment root, as for
# modules/compute-stack. archive builds the reaper's deployment package from
# reaper/reaper.py at plan time, so no zip is ever committed.
terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.7"
    }
  }
}
