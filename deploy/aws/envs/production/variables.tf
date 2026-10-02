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

variable "compute_api_image" {
  type    = string
  default = "ghcr.io/felixsphere/unify-compute-api@sha256:10cf58392547078e2134f5ff0e1a0c9b0416ca7e186746f07e243697ed58ce4a"
}

variable "site_image" {
  type    = string
  default = "ghcr.io/felixsphere/unify-compute-site@sha256:a918fe89b1cc2227ad486ef9391526529c90aa770fb0d2b4f0526ed5d6b84c0a"
}

variable "ami_id" {
  description = "Empty for the first apply only; then pin the ami_id_in_use output here."
  type        = string
  default     = ""
}
