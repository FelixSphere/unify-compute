# One module, two callers: deploy/aws/envs/staging and deploy/aws/envs/production.
# Defaults here are the ones both environments share. Anything that differs
# between them (hostnames, image pins, whether a console runs, backups,
# retention) has no default, so a new caller has to decide it.

variable "region" {
  description = "us-east-1 to match the rest of UnifyAI's AWS footprint (gateway stack, marketing infra, Unify Data)."
  type        = string
}

variable "environment" {
  description = "staging or production. Written into the instance tags that the GitHub deploy roles match on (deploy/aws/shared/github-oidc)."
  type        = string

  validation {
    condition     = contains(["staging", "production"], var.environment)
    error_message = "environment must be staging or production; the deploy roles match on exactly these values."
  }
}

variable "name" {
  description = "Prefix for every resource in this stack and for the SSM parameter path (/<name>/<key>)."
  type        = string
}

variable "hosted_zone" {
  description = "Route53 zone the hostnames live in."
  type        = string
  default     = "unifyai.us"
}

variable "api_domain" {
  description = "Public hostname for compute-api."
  type        = string
}

variable "site_domain" {
  description = "Public hostname for the marketing site container."
  type        = string
}

variable "manage_site_dns" {
  description = <<-EOT
    Whether this stack owns the A alias for site_domain. Production passes
    false until the deliberate cutover from OpenAI Sites (ADR-0009): the
    existing compute.unifyai.us record is a CNAME this stack must not touch on
    its first apply. The certificate covers site_domain either way, because its
    validation records are children of the name, not the name itself.
  EOT
  type        = bool
}

variable "console_domain" {
  description = "Public hostname for a console that runs on this instance. Null means no console container, no Redis, and no console routes — production, whose console is app.unifyapi.ai and belongs to the UnifyAPI release owner."
  type        = string
  default     = null
}

variable "console_url" {
  description = "Where compute-api reaches the console. The compose service name when the console runs here; the public production console otherwise."
  type        = string
}

variable "bridge_enabled" {
  description = <<-EOT
    Whether user-data reads /<name>/bridge_secret into COMPUTE_BRIDGE_SECRET.
    False keeps compute-api's console bridge dark no matter what parameters
    exist. In production this stays false until the
    console bridge is released on app.unifyapi.ai; turning it on is a reviewed
    change here, not a parameter someone creates.
  EOT
  type        = bool
}

variable "instance_type" {
  description = "arm64 (t4g). Every pinned image must publish arm64; CI's pinned-images job checks. ~USD 12/month on-demand."
  type        = string
  default     = "t4g.small"
}

variable "ami_id" {
  description = <<-EOT
    Pinned AL2023 arm64 AMI. Pinned rather than looked up from the SSM
    "latest" pointer because that pointer moves within days and `ami` forces
    instance replacement — the UnifyAPI console stack armed a production
    destroy that way. Empty means look up the latest, which is acceptable only
    for the very first apply; pin it immediately afterwards from the
    ami_id_in_use output.
  EOT
  type        = string
  default     = ""
}

variable "vpc_cidr" {
  type = string
}

variable "data_volume_gb" {
  description = "EBS volume mounted at /data: Postgres, console data, SkyPilot state."
  type        = number
  default     = 30
}

variable "ingress_cidrs" {
  description = "Who may reach the load balancer."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "compute_api_image" {
  description = "compute-api image, by digest."
  type        = string
}

variable "site_image" {
  description = "Marketing site image, by digest. Listens on :8080 and answers GET /healthz."
  type        = string
}

variable "console_image" {
  description = "Console fork image, by digest. Required when console_domain is set, ignored otherwise."
  type        = string
  default     = null
}

variable "hsts_max_age" {
  description = "Strict-Transport-Security max-age the ALB adds to every HTTPS response. No includeSubDomains, no preload: both are commitments about names this stack does not own."
  type        = number
  default     = 31536000
}

variable "alb_deletion_protection" {
  type = bool
}

variable "alb_log_retention_days" {
  type = number
}

variable "alb_logs_force_destroy" {
  description = "True lets `terraform destroy` empty the logs bucket. Staging only: production access logs are the record of who called what."
  type        = bool
}

variable "snapshot_backups" {
  description = "Daily EBS snapshots of the data volume via DLM (see backup.tf for why snapshots and not pg_dump)."
  type        = bool
}

variable "snapshot_retain_count" {
  description = "How many daily snapshots DLM keeps."
  type        = number
  default     = 14
}

# SSM SecureString parameter names. Created out of band with
# scripts/staging-secrets.sh, never as Terraform resources: aws_ssm_parameter
# would write the values into state.
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
