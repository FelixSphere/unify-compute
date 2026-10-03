# Unify Compute's own mail identity (ADR-0011, section 6): compute-api sends
# account email (verification codes, resets, key-created notices) as
# no-reply@compute.unifyai.us, from both environments.
#
# Its own stack, like ecr and gpu-budget: one identity serves staging and
# production, so it belongs to neither environment's state, and destroying an
# environment must not unverify the other's sender.
#
# What this separates from the other products in account 871376691052
# (Unify API's unifyapi.ai, Unify Data's data.unifyai.us, the gateway): the
# identity, its DKIM keys, and the configuration set, so compute's bounce and
# complaint rates are measured on their own and its sending can be switched
# off on its own. What it does not separate: production access, the daily
# quota and an account-level suspension are per account and region. A
# suspension earned by any product stops compute's signup codes too.
#
# Production access already exists (checked 2026-10-03: 50,000 per 24 h,
# 14/s, HEALTHY), so there is no sandbox step.
#
# The instance roles are granted ses:SendEmail on this identity and on this
# configuration set by deploy/aws/modules/compute-stack/ses.tf, which builds
# both ARNs from the same names instead of reading this state.

locals {
  # Also named in deploy/aws/modules/compute-stack/variables.tf
  # (ses_identity, ses_configuration_set, ses_from_address).
  domain            = "compute.unifyai.us"
  configuration_set = "unify-compute"
  from_address      = "no-reply@${local.domain}"
  mail_from_domain  = "bounce.${local.domain}"
}

data "aws_route53_zone" "this" {
  name         = var.hosted_zone
  private_zone = false
}

resource "aws_sesv2_configuration_set" "this" {
  configuration_set_name = local.configuration_set

  reputation_options {
    reputation_metrics_enabled = true
  }

  # The per-product kill switch: `aws sesv2 put-configuration-set-sending-options
  # --configuration-set-name unify-compute --no-sending-enabled` stops every
  # compute send (it is the identity's default set, below) without touching
  # another product. Terraform would turn it back on at the next apply, which
  # is what you want once the cause is fixed.
  sending_options {
    sending_enabled = true
  }

  tags = { product = "unify-compute" }
}

# The same shape as Unify Data's (configuration set unifydata): CloudWatch
# metrics per event type under the configuration-set dimension. Bounce and
# complaint rates are what SES judges the account on; DELIVERY and SEND give
# the denominator.
resource "aws_sesv2_configuration_set_event_destination" "cloudwatch" {
  configuration_set_name = aws_sesv2_configuration_set.this.configuration_set_name
  event_destination_name = "cloudwatch-delivery-health"

  event_destination {
    enabled = true
    matching_event_types = [
      "SEND",
      "DELIVERY",
      "DELIVERY_DELAY",
      "BOUNCE",
      "COMPLAINT",
      "REJECT",
      "RENDERING_FAILURE",
    ]

    cloud_watch_destination {
      dimension_configuration {
        dimension_name          = "ses:configuration-set"
        dimension_value_source  = "MESSAGE_TAG"
        default_dimension_value = local.configuration_set
      }
    }
  }
}

# The configuration set is the identity's default, so a send that names no
# set still gets it and the mailer cannot forget it. The consequence, which
# Unify Data found in production: SES then authorises the configuration set
# as a resource of every send, so the sender needs ses:SendEmail on its ARN
# as well as the identity's (modules/compute-stack/ses.tf).
resource "aws_sesv2_email_identity" "this" {
  email_identity         = local.domain
  configuration_set_name = aws_sesv2_configuration_set.this.configuration_set_name

  # Easy DKIM. 2048-bit, as data.unifyai.us uses; stated rather than left to
  # the API default so a new identity cannot quietly get a shorter key.
  dkim_signing_attributes {
    next_signing_key_length = "RSA_2048_BIT"
  }

  tags = { product = "unify-compute" }
}

# Easy DKIM always issues three tokens. count, not for_each: the tokens are
# unknown until the identity exists, and for_each keys must be known at plan.
#
# These are children of compute.unifyai.us (<token>._domainkey...), never the
# name itself, so the A alias at compute.unifyai.us is untouched. Route53
# refuses to create a record that already exists (allow_overwrite is off), so
# an apply cannot replace anything it did not create.
resource "aws_route53_record" "dkim" {
  count = 3

  zone_id = data.aws_route53_zone.this.zone_id
  name    = "${aws_sesv2_email_identity.this.dkim_signing_attributes[0].tokens[count.index]}._domainkey.${local.domain}"
  type    = "CNAME"
  ttl     = 1800
  records = ["${aws_sesv2_email_identity.this.dkim_signing_attributes[0].tokens[count.index]}.dkim.amazonses.com"]
}

# A custom MAIL FROM so the envelope sender is ours: SPF then passes for
# bounce.compute.unifyai.us, which is aligned with the From domain under
# DMARC's relaxed rule. Without it the envelope is amazonses.com, SPF never
# aligns, and DMARC rests on DKIM alone.
resource "aws_sesv2_email_identity_mail_from_attributes" "this" {
  email_identity   = aws_sesv2_email_identity.this.email_identity
  mail_from_domain = local.mail_from_domain

  # If the MX below is ever missing, SES falls back to amazonses.com and keeps
  # sending (DKIM still aligns) instead of rejecting every message.
  behavior_on_mx_failure = "USE_DEFAULT_VALUE"
}

resource "aws_route53_record" "mail_from_mx" {
  zone_id = data.aws_route53_zone.this.zone_id
  name    = local.mail_from_domain
  type    = "MX"
  ttl     = 1800
  records = ["10 feedback-smtp.${var.region}.amazonses.com"]
}

resource "aws_route53_record" "mail_from_spf" {
  zone_id = data.aws_route53_zone.this.zone_id
  name    = local.mail_from_domain
  type    = "TXT"
  ttl     = 1800
  records = ["v=spf1 include:amazonses.com ~all"]
}

# p=none: report, do not enforce, until the reports show only our own mail
# (ADR-0011: none first, then quarantine). No record existed at this name on
# 2026-10-03 (read-only check of the zone), and none at _dmarc.unifyai.us
# either, so receivers had no policy for compute.unifyai.us at all.
resource "aws_route53_record" "dmarc" {
  zone_id = data.aws_route53_zone.this.zone_id
  name    = "_dmarc.${local.domain}"
  type    = "TXT"
  ttl     = 1800
  records = ["v=DMARC1; p=none; rua=mailto:${var.dmarc_report_email}"]
}
