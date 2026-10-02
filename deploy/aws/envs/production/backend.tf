# Remote state with locking.
#
# State was local, and versions.tf was honest about why that was a problem: it
# is the only record of what was applied. Local state means one laptop can
# manage this stack, a second `apply` from anywhere else creates a parallel
# universe, and losing the file loses the environment.
#
# This is set up before the first apply on purpose. Nothing is applied yet, so
# there is no state to migrate — the same change after the fact is a migration
# with a window where two states disagree.
#
# `use_lockfile` is S3-native locking (Terraform >= 1.10); it replaces the
# DynamoDB table the old pattern needed. Bootstrap the bucket first:
#
#     scripts/tf-backend-bootstrap.sh
#
# Same bucket as staging, separate key: one state per environment, so a
# staging apply can never plan against production resources.
#
terraform {
  backend "s3" {
    bucket       = "unifyai-tfstate-871376691052"
    key          = "unify-compute/production/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
