output "user_name" {
  description = "The IAM user to create an access key for, by hand (see main.tf)."
  value       = aws_iam_user.agent.name
}

output "user_arn" {
  description = "What `aws sts get-caller-identity` should report once the key is in the agent's environment."
  value       = aws_iam_user.agent.arn
}
