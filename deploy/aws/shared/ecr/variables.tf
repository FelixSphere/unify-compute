variable "region" {
  type    = string
  default = "us-east-1"
}

variable "keep_images" {
  description = "Images kept per repository. Only released digests are copied here (one tagged index per release and repository), so this is roughly the last N releases; the hosts' rollback target is one release back."
  type        = number
  default     = 30
}
