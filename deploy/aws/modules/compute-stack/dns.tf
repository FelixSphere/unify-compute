# One certificate per environment covering every hostname it serves, validated
# in Route53, and an alias record per hostname — except the site's, which
# production leaves alone until the cutover (var.manage_site_dns).

data "aws_route53_zone" "this" {
  name         = var.hosted_zone
  private_zone = false
}

locals {
  # The API name is the certificate's primary name in both environments;
  # every other hostname is a SAN.
  cert_sans = [for k, a in local.apps : a.domain if k != "api"]

  dns_records = {
    for k, a in local.apps : k => a.domain
    if k != "site" || var.manage_site_dns
  }
}

resource "aws_acm_certificate" "this" {
  domain_name               = var.api_domain
  subject_alternative_names = local.cert_sans
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }

  tags = { Name = var.name }
}

# Validation records are _<hash>.<name> — children of each hostname, never the
# hostname itself. That is why production can validate compute.unifyai.us
# while compute.unifyai.us is still a CNAME to OpenAI Sites: Route53 allows
# records below a CNAME'd name (the existing _cf-custom-hostname and
# _openai-site-verification TXT records under it are the same shape).
resource "aws_route53_record" "validation" {
  for_each = {
    for o in aws_acm_certificate.this.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      record = o.resource_record_value
      type   = o.resource_record_type
    }
  }

  zone_id         = data.aws_route53_zone.this.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [for r in aws_route53_record.validation : r.fqdn]
}

# If manage_site_dns is turned on while the name still holds the OpenAI Sites
# CNAME, Route53 rejects this create ("conflicting RRSet of type CNAME") and
# the apply fails without touching the live record. That failure is the
# guard; the cutover itself is an atomic change batch (ADR-0009), not this.
resource "aws_route53_record" "app" {
  for_each = local.dns_records

  zone_id = data.aws_route53_zone.this.zone_id
  name    = each.value
  type    = "A"

  alias {
    name                   = aws_lb.this.dns_name
    zone_id                = aws_lb.this.zone_id
    evaluate_target_health = false
  }
}
