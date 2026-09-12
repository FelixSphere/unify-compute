variable "region" {
  description = "us-east-1 to match the rest of UnifyAI's AWS footprint (gateway stack, marketing infra)."
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  type    = string
  default = "staging"
}

variable "name" {
  description = "Prefix for every resource in this stack and for the SSM parameter path."
  type        = string
  default     = "unify-compute-staging"
}

variable "hosted_zone" {
  description = "Route53 zone the hostnames live in. compute.unifyai.us itself stays a CNAME to OpenAI Sites in this zone; only child records are created here."
  type        = string
  default     = "unifyai.us"
}

variable "compute_domain" {
  description = "Public hostname for compute-api."
  type        = string
  default     = "staging.compute.unifyai.us"
}

variable "console_domain" {
  description = "Public hostname for the staging UnifyAPI console. Its own database; never production's."
  type        = string
  default     = "console.staging.compute.unifyai.us"
}

variable "instance_type" {
  description = "arm64 (t4g). Both the console fork image and compute-api publish arm64; Graviton is ~20% cheaper. ~USD 12/month on-demand."
  type        = string
  default     = "t4g.small"
}

variable "ami_id" {
  description = <<-EOT
    Pinned AL2023 arm64 AMI. Pinned rather than looked up from the SSM
    "latest" pointer because that pointer moves within days and `ami` forces
    instance replacement — the UnifyAPI console stack armed a production
    destroy that way. Empty means look up the latest, which is acceptable only
    for the very first apply; pin it immediately afterwards from the plan output.
  EOT
  type        = string
  default     = ""
}

variable "vpc_cidr" {
  type    = string
  default = "10.70.0.0/16"
}

variable "data_volume_gb" {
  description = "EBS volume mounted at /data: Postgres for both databases, console data, compute-api state."
  type        = number
  default     = 30
}

variable "ingress_cidrs" {
  description = "Who may reach the load balancer. Open for staging so agents and the smoke script can reach it from anywhere; narrow if it becomes a problem."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "console_image" {
  description = "Console fork image. Same pin as UnifyAPI production unless testing a console change."
  type        = string
  default     = "ghcr.io/felixsphere/unifyapi-console:unifyapi-v0.1.52"
}

variable "compute_api_image" {
  description = "compute-api image. Releases swap this on the instance over SSM first, then bump here so the next rebuild lands on it (see .claude/agents/cicd.md)."
  type        = string
  default     = "ghcr.io/felixsphere/unify-compute-api:dev"
}

# SSM SecureString parameter names. Created out of band with
# `aws ssm put-parameter --type SecureString`, never as Terraform resources:
# aws_ssm_parameter would write the values into this local state file.
#
# Null means "/<var.name>/<key>" (see locals in instance.tf). The IAM read
# policy is scoped to that same prefix, so a stack that renames `name` keeps
# its parameter names and its permissions in step. Override only if the
# parameters live somewhere else; then widen the policy to match.
variable "ssm_postgres_password" {
  type    = string
  default = null
}

variable "ssm_redis_password" {
  type    = string
  default = null
}

variable "ssm_session_secret" {
  type    = string
  default = null
}

variable "ssm_bridge_secret" {
  type    = string
  default = null
}
