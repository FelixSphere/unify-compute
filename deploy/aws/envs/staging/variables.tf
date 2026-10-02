variable "region" {
  type    = string
  default = "us-east-1"
}

# Image pins for staging. These defaults are the only place staging's pins
# live; CI's pinned-images job reads them and requires each to be a digest
# that exists in the registry with a linux/arm64 manifest.
#
# A pin that is not a digest yet (":dev", ":unpublished") keeps that job red
# on purpose: it is the truthful state of "this stack cannot boot yet", and
# the job going green is the signal that it can.

variable "compute_api_image" {
  description = "Publish with .github/workflows/publish-image.yml, then pin the digest it prints."
  type        = string
  default     = "ghcr.io/felixsphere/unify-compute-api@sha256:10cf58392547078e2134f5ff0e1a0c9b0416ca7e186746f07e243697ed58ce4a"
}

variable "site_image" {
  description = "Marketing site image, by digest. Rebuild baseline only; releases swap it on the host."
  type        = string
  default     = "ghcr.io/felixsphere/unify-compute-site@sha256:a918fe89b1cc2227ad486ef9391526529c90aa770fb0d2b4f0526ed5d6b84c0a"
}

variable "console_image" {
  description = "Console fork image. Same release as UnifyAPI production unless testing a console change; tag kept for readability, digest is what is pulled."
  type        = string
  default     = "ghcr.io/felixsphere/unifyapi-console:unifyapi-v0.1.52@sha256:c22a1bfed2da54f33dc9b5dcb498fd18fd9949248284f2022e410e43df9ece87"
}

variable "ami_id" {
  description = "Pinned from ami_id_in_use after the first apply (2026-10-02). Moving it replaces the instance."
  type        = string
  default     = "ami-065b1b834d2a83a7a"
}
