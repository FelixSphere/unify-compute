# The GPU workers for one environment: a VPC that holds nothing else, the role
# SkyPilot launches with, the powerless role the workers run as, and the reaper
# that terminates anything SkyPilot and compute-api both failed to clean up.
# Why this lives in the shared AWS account, and why it is safe to, is
# docs/adr/0010-gpu-workers-in-the-shared-account.md.

variable "name" {
  description = "The compute stack's name (unify-compute-<environment>). Every name here derives from it, so the SkyPilot config the host renders and the resources here cannot drift apart."
  type        = string
}

variable "environment" {
  description = "staging or production. Becomes the workers' Environment tag, which every mutating permission of the launch role is conditioned on."
  type        = string

  validation {
    condition     = contains(["staging", "production"], var.environment)
    error_message = "environment must be staging or production."
  }
}

variable "region" {
  type = string
}

variable "vpc_cidr" {
  description = "Must be unique in the account. In use on 2026-10-03: 10.20 (unifyapi-console), 10.60 (gateway), 10.70 (unifydata), 10.71/10.72 (compute production/staging), 10.80/10.81 (SelfAI), 172.31 (default VPC). Reserved here: 10.73 staging workers, 10.74 production workers."
  type        = string
}

variable "availability_zones" {
  description = "One public subnet per zone. us-east-1e is left out because it does not offer g5 (describe-instance-type-offerings, 2026-10-03); a subnet SkyPilot cannot use for the SKU only slows its zone failover."
  type        = list(string)
}

variable "instance_types" {
  description = "The only instance types the launch role may start. Adding one is a reviewed change with money in it, like a SKU."
  type        = list(string)

  validation {
    condition     = length(var.instance_types) > 0
    error_message = "At least one instance type, or there is nothing to launch."
  }

  # ADR-0010, "Start small" (2026-10-03): one g5.xlarge is the whole fleet,
  # and the AWS quota, the budget and the overshoot bound are sized for it.
  # Widening this is an amendment to that ADR first, then this line.
  validation {
    condition     = alltrue([for t in var.instance_types : t == "g5.xlarge"])
    error_message = "Only g5.xlarge may be launched while the platform starts small (ADR-0010)."
  }
}

variable "api_server_ip" {
  description = "The compute host's Elastic IP. The only address allowed to reach a worker (SSH, which is how the SkyPilot API server drives it)."
  type        = string
}

variable "api_server_role_arn" {
  description = "The compute host's instance role. The only principal that may assume the launch role."
  type        = string
}

variable "reaper_margin_hours" {
  description = "Added to the platform runtime ceiling (compute-api/config/platform.json) before the reaper terminates a worker: covers provisioning (not billed, but the instance exists), compute-api's own teardown, and its retries. SkyPilot's launch-to-UP is minutes, so one hour is ample and every hour here is up to USD ~1.04 per orphaned worker."
  type        = number
  default     = 1
}

variable "max_concurrent_workers" {
  description = "How many workers may run at once in this environment before the reaper reports it. IAM cannot count, so this is a report, not an enforcement; the hard cap is the account's G/VT vCPU quota (ADR-0010)."
  type        = number
  default     = 1
}

variable "alert_email" {
  description = "Where the reaper's reports go (SNS email; the address must confirm the subscription). Null leaves the topic with no subscriber, and the reports only in CloudWatch Logs."
  type        = string
  default     = null
}
