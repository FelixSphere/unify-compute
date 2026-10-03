# Same bucket as the other stacks, its own key. See
# deploy/aws/envs/staging/backend.tf for why state is remote from the start.
terraform {
  backend "s3" {
    bucket       = "unifyai-tfstate-871376691052"
    key          = "unify-compute/shared/gpu-budget/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
