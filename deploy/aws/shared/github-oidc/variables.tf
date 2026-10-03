variable "region" {
  type    = string
  default = "us-east-1"
}

variable "create_oidc_provider" {
  description = "False uses the account's existing GitHub OIDC provider (see main.tf for how to check). True creates one; only ever for an account that has none."
  type        = bool
  default     = false
}

variable "github_subject_prefix" {
  description = <<-EOT
    The OIDC `sub` prefix GitHub issues for this repository. FelixSphere repos
    use immutable subjects (owner and repo IDs, not names), so a trust policy
    on "repo:FelixSphere/unify-compute:..." would never match. Read the real
    value with:

        gh api repos/FelixSphere/unify-compute/actions/oidc/customization/sub

    and use its sub_claim_prefix. A renamed repository keeps this value; a
    deleted-and-recreated one does not, which is the point.
  EOT
  type        = string
  default     = "repo:FelixSphere@164092079/unify-compute@1364156790"
}

variable "environments" {
  description = "GitHub environments that get a deploy role. Each name must match the Environment tag on that stack's instance."
  type        = list(string)
  default     = ["staging", "production"]
}
