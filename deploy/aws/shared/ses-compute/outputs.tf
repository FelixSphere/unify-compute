output "identity" {
  value = aws_sesv2_email_identity.this.email_identity
}

output "identity_arn" {
  value = aws_sesv2_email_identity.this.arn
}

output "configuration_set" {
  value = aws_sesv2_configuration_set.this.configuration_set_name
}

output "configuration_set_arn" {
  value = aws_sesv2_configuration_set.this.arn
}

output "from_address" {
  description = "The only From address the instance roles may send as (modules/compute-stack/ses.tf)."
  value       = local.from_address
}

output "mail_from_domain" {
  value = aws_sesv2_email_identity_mail_from_attributes.this.mail_from_domain
}

output "dkim_records" {
  value = aws_route53_record.dkim[*].fqdn
}
