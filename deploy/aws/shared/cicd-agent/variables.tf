variable "region" {
  type    = string
  default = "us-east-1"
}

variable "environments" {
  description = "Hosts the agent may run commands on, by environment name (instance tag Name=unify-compute-<env>). Staging only until the operator decides otherwise: adding production gives the agent a root shell on the production host."
  type        = list(string)
  default     = ["staging"]

  validation {
    condition     = alltrue([for e in var.environments : contains(["staging", "production"], e)])
    error_message = "environments may only name staging and production."
  }
}
