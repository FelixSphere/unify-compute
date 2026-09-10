output "compute_url" {
  value = "https://${var.compute_domain}"
}

output "console_url" {
  value = "https://${var.console_domain}"
}

output "instance_id" {
  description = "For `aws ssm start-session --target <id>`."
  value       = aws_instance.this.id
}

output "ami_id_in_use" {
  description = "Pin this into var.ami_id after the first apply so a moved 'latest' pointer cannot force a replacement."
  value       = local.ami_id
}

output "alb_dns_name" {
  value = aws_lb.this.dns_name
}
