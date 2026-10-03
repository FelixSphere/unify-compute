# Consumed by modules/compute-stack through the environment root. Nothing here
# depends on the compute stack's outputs except through resources (the trust
# policy, the security group), so the two modules do not form a cycle.

output "skypilot_config" {
  description = "~/.sky/config.yaml for the SkyPilot API server."
  value       = local.skypilot_config
}

output "launch_role_arn" {
  description = "The role the API server's credentials are assumed from."
  value       = local.launch_role_arn
}

output "vpc_id" {
  value = aws_vpc.workers.id
}

output "security_group_id" {
  value = aws_security_group.workers.id
}

output "reaper_topic_arn" {
  value = aws_sns_topic.reaper.arn
}
