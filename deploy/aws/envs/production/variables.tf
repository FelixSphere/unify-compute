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
  # True since the cutover on 2026-10-02 (Route53 change C07235422LT740FCOOAEW);
  # the record was imported, not created. Rolling back DNS means setting this
  # false again after `terraform state rm` (ADR-0009, "Rollback").
  default = true
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
  default = "871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea"
}

variable "ami_id" {
  description = "Empty for the first apply only; then pin the ami_id_in_use output here."
  type        = string
  default     = ""
}

variable "gpu_workers_enabled" {
  description = "The SkyPilot API server on the production host and production's GPU workers (ADR-0010). False until staging has run real jobs through the same setup."
  type        = bool
  default     = false
}

variable "gpu_alert_email" {
  description = "Where production's GPU reaper reports go, once gpu_workers_enabled."
  type        = string
  default     = null
}
