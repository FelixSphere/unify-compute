variable "region" {
  type    = string
  default = "us-east-1"
}

variable "alert_email" {
  description = "Who is told when GPU worker spend crosses a threshold. No default: a budget nobody hears is not a control."
  type        = string
}

variable "monthly_limit_usd" {
  description = "Monthly spend on everything tagged Project=unify-compute-workers, both environments. At 100% of actual spend the budget action denies new launches (main.tf). 50 is about 48 g5.xlarge hours at USD 1.039 all-in: enough for staging's first jobs, and small enough that the operator never meets a surprise (ADR-0010, \"Start small\"). Raise it deliberately, with the AWS quota and compute-api/config/platform.json."
  type        = number
  default     = 50

  validation {
    condition     = var.monthly_limit_usd > 0
    error_message = "A zero budget would deny every launch from the first cent; use platform.json's max_active_jobs = 0 to stop launches instead."
  }
}

variable "launch_role_names" {
  description = "The SkyPilot launch roles the budget action denies RunInstances/StartInstances to once the month's budget is spent. Staging's only: production's role does not exist while gpu_workers_enabled = false, and an action naming a missing role fails when it fires. Add unify-compute-production-skypilot in the same change that enables production's workers."
  type        = list(string)
  default     = ["unify-compute-staging-skypilot"]

  validation {
    condition     = length(var.launch_role_names) > 0 && alltrue([for r in var.launch_role_names : can(regex("^unify-compute-(staging|production)-skypilot$", r))])
    error_message = "Only the unify-compute launch roles; the execution role may attach the deny policy to nothing else."
  }
}
