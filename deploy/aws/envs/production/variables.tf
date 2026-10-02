variable "region" {
  type    = string
  default = "us-east-1"
}

variable "manage_site_dns" {
  description = <<-EOT
    Whether this stack owns the compute.unifyai.us A alias. Leave false until
    the cutover in ADR-0009 has been run: the name is a CNAME to OpenAI Sites
    today, and the cutover replaces it with one atomic Route53 change batch,
    after which the record is imported and this is set true. Setting it true
    before then fails the apply (Route53 refuses an A next to a CNAME) without
    touching the live record.
  EOT
  type        = bool
  default     = false
}

# Image pins for production. Same rule as staging: digests only, checked by
# CI's pinned-images job, red until each has been published. Production should
# pin a digest staging has already run.
# Ours are ECR refs with the GHCR build's digest; see staging/variables.tf.

variable "compute_api_image" {
  type    = string
  default = "871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902"
}

variable "site_image" {
  type    = string
  default = "871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018"
}

variable "ami_id" {
  description = "Empty for the first apply only; then pin the ami_id_in_use output here."
  type        = string
  default     = ""
}
