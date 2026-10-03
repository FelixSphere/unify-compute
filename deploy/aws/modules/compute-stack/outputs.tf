output "urls" {
  description = "Every hostname this stack routes, by app."
  value       = { for k, a in local.apps : k => "https://${a.domain}" }
}

output "api_origin" {
  value = "https://${var.api_domain}"
}

output "site_origin" {
  description = "Where the site answers today: its own name once this stack manages it, otherwise the first alias (production before the cutover)."
  value       = var.manage_site_dns || length(var.site_aliases) == 0 ? "https://${var.site_domain}" : "https://${var.site_aliases[0]}"
}

output "instance_id" {
  description = "For `aws ssm send-command --instance-ids <id>`."
  value       = aws_instance.this.id
}

output "ami_id_in_use" {
  description = "Pin this into ami_id after the first apply so a moved 'latest' pointer cannot force a replacement."
  value       = local.ami_id
}

output "alb_dns_name" {
  value = aws_lb.this.dns_name
}

output "alb_zone_id" {
  description = "With alb_dns_name, the alias target for the site cutover change batch."
  value       = aws_lb.this.zone_id
}

output "data_volume_id" {
  value = aws_ebs_volume.data.id
}

output "instance_role_arn" {
  description = "The only principal the GPU workers' launch role trusts (modules/gpu-workers)."
  value       = aws_iam_role.instance.arn
}

output "public_ip" {
  description = "The Elastic IP. The only address GPU workers admit SSH from."
  value       = aws_eip.instance.public_ip
}

output "skypilot_host_setup" {
  description = "The SkyPilot setup payload for scripts/ssm-run.sh, for a host that booted before SkyPilot was enabled. Empty when it is not."
  value       = local.skypilot_host_setup
}
