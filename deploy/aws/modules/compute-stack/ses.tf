# compute-api sends account email through SES (ADR-0011, section 6) with this
# instance's role. The identity and its configuration set are created once for
# both environments by deploy/aws/shared/ses-compute; the ARNs here are built
# from the same names rather than read from that state, as with ECR.
#
# Its own policy rather than a statement in the secrets policy, so adding it
# is a create and leaves the policy every boot depends on untouched.

locals {
  ses_identity_arn          = "arn:aws:ses:${var.region}:${data.aws_caller_identity.current.account_id}:identity/${var.ses_identity}"
  ses_configuration_set_arn = "arn:aws:ses:${var.region}:${data.aws_caller_identity.current.account_id}:configuration-set/${var.ses_configuration_set}"
}

data "aws_iam_policy_document" "instance_ses" {
  statement {
    sid     = "SendAsComputeNoReplyOnly"
    actions = ["ses:SendEmail", "ses:SendRawEmail"]
    # Both ARNs, not the identity alone. The configuration set is the
    # identity's default, so SES applies it to every send and authorises it as
    # a resource; with only the identity granted, every send is AccessDenied on
    # configuration-set/<name>, and nobody can finish signing up (Unify Data,
    # deploy/README.md, "Sending is separated from the other products").
    resources = [local.ses_identity_arn, local.ses_configuration_set_arn]

    # The identity is the whole domain, which would otherwise let this role
    # send as any address at it. The second value covers a From carrying a
    # display name ("Unify Compute <no-reply@...>"), in case SES evaluates the
    # key on the header form rather than the bare address; either way the
    # address itself is fixed.
    condition {
      test     = "StringLike"
      variable = "ses:FromAddress"
      values   = [var.ses_from_address, "*<${var.ses_from_address}>"]
    }
  }
}

resource "aws_iam_role_policy" "instance_ses" {
  name   = "${var.name}-ses"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance_ses.json
}

# Names, not secrets, like backup_bucket and artifacts_bucket: in the
# instance's own SSM prefix, so the role reads them with the policy it already
# has, and a host or release step can render them into .env without a
# Terraform output. compute-api does not read them yet; the mailer (plan task
# A2) and the staging switch to local accounts (A8) wire them in.
resource "aws_ssm_parameter" "ses_identity" {
  name  = "${local.ssm_prefix}/ses_identity"
  type  = "String"
  value = var.ses_identity
}

resource "aws_ssm_parameter" "ses_configuration_set" {
  name  = "${local.ssm_prefix}/ses_configuration_set"
  type  = "String"
  value = var.ses_configuration_set
}

# The full From header. It contains a space and "<": docker compose's .env
# parser takes that as is, but anything that sources the file with a shell
# needs it single-quoted (Unify Data's .env broke that way).
resource "aws_ssm_parameter" "ses_sender" {
  name  = "${local.ssm_prefix}/ses_sender"
  type  = "String"
  value = "${var.ses_sender_name} <${var.ses_from_address}>"
}
