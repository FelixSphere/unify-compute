output "deploy_role_arns" {
  description = "Set each as the AWS_DEPLOY_ROLE_ARN variable of the matching GitHub environment."
  value       = { for env, role in aws_iam_role.deploy : env => role.arn }
}
