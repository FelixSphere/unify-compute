output "deploy_role_arns" {
  description = "Per GitHub environment: store as the AWS_DEPLOY_ROLE_ARN secret of that environment (.github/workflows/release.yml reads secrets.AWS_DEPLOY_ROLE_ARN)."
  value       = { for env, role in aws_iam_role.deploy : env => role.arn }
}
