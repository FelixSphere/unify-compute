variable "region" {
  type    = string
  default = "us-east-1"
}

variable "alert_email" {
  description = "Who is told when GPU worker spend crosses a threshold. No default: a budget nobody hears is not a control."
  type        = string
}

variable "monthly_limit_usd" {
  description = "Monthly spend on everything tagged Project=unify-compute-workers, both environments. 300 is about 290 g5.xlarge hours at USD 1.006: generous for staging's first jobs, and an alarm long before it is a bill. Raise it deliberately, with the quota."
  type        = number
  default     = 300
}
