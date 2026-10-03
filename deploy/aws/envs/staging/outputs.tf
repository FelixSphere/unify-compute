output "api_origin" {
  description = "PUBLIC_ORIGIN for API smoke checks."
  value       = module.stack.api_origin
}

output "site_origin" {
  description = "PUBLIC_ORIGIN for site smoke checks: where the site answers on AWS today."
  value       = module.stack.site_origin
}

output "app_origin" {
  description = "APP_ORIGIN for the app host's smoke checks (scripts/release/smoke.sh)."
  value       = module.stack.app_origin
}

output "github_environment" {
  description = "Variables for this GitHub environment (.github/workflows/release.yml). The AWS_DEPLOY_ROLE_ARN secret comes from deploy/aws/shared/github-oidc."
  value = {
    AWS_REGION   = var.region
    ALB_DNS_NAME = module.stack.alb_dns_name
  }
}

output "urls" {
  value = module.stack.urls
}

output "instance_id" {
  value = module.stack.instance_id
}

output "ami_id_in_use" {
  value = module.stack.ami_id_in_use
}

output "alb_dns_name" {
  value = module.stack.alb_dns_name
}

output "alb_zone_id" {
  value = module.stack.alb_zone_id
}

output "data_volume_id" {
  value = module.stack.data_volume_id
}

output "skypilot_host_setup" {
  description = "Payload for the one-time SkyPilot setup on the running host: terraform output -raw skypilot_host_setup > setup.sh && scripts/ssm-run.sh <instance> setup.sh ... (docs/staging.md)."
  value       = module.stack.skypilot_host_setup
}

output "gpu_workers_vpc_id" {
  value = module.gpu_workers.vpc_id
}

output "gpu_reaper_topic_arn" {
  description = "Subscribe to this for reaper reports if gpu_alert_email is not set."
  value       = module.gpu_workers.reaper_topic_arn
}
