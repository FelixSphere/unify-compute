variable "region" {
  type    = string
  default = "us-east-1"
}

# Image pins for staging. These defaults are the only place staging's pins
# live; CI's pinned-images job reads them and requires each to be a digest
# that exists in the registry with a linux/arm64 manifest.
#
# Ours point at the runtime registry (ECR, deploy/aws/shared/ecr), which is
# what the instance can pull from. The digest is the GHCR build's, unchanged:
# release.yml copies it across before any cutover, and CI checks it in the
# GHCR repository of the same name. A fresh instance can boot from a pin only
# once some release has copied that digest into ECR.
#
# A pin that is not a digest yet (":dev", ":unpublished") keeps that job red
# on purpose: it is the truthful state of "this stack cannot boot yet", and
# the job going green is the signal that it can.

variable "compute_api_image" {
  description = "Publish with .github/workflows/publish-image.yml; pin the digest it prints, under the ECR repository of the same name."
  type        = string
  default     = "871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902"
}

variable "site_image" {
  description = "Marketing site image, by digest. Rebuild baseline only; releases swap it on the host."
  type        = string
  default     = "871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea"
}

variable "console_image" {
  description = "Console fork image. Same release as UnifyAPI production unless testing a console change; tag kept for readability, digest is what is pulled."
  type        = string
  default     = "ghcr.io/felixsphere/unifyapi-console:e3cef17d71061ac534758cb220960bb265748cd3@sha256:c48c638be295a61909532d101b98635903f232bfb106c305c12ef7b6f84ed3ab"
}

variable "ami_id" {
  description = "Pinned from ami_id_in_use after the first apply (2026-10-02). Moving it replaces the instance."
  type        = string
  default     = "ami-065b1b834d2a83a7a"
}

variable "gpu_alert_email" {
  description = "Where the GPU reaper's reports go. Null keeps them in CloudWatch Logs only; set it before the first GPU launch (the address has to confirm the SNS subscription)."
  type        = string
  default     = null
}
