# Same bucket as the environment stacks, its own key. See
# deploy/aws/envs/staging/backend.tf for why state is remote from the start.
terraform {
  backend "s3" {
    bucket       = "unifyai-tfstate-871376691052"
    key          = "unify-compute/shared/cicd-agent/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
