# GitHub Actions deploy roles, one per environment, assumable only by this
# repository's workflows running in the matching GitHub environment. No static
# keys anywhere. Same pattern as Unify Data's `unifydata-deploy` role.
#
# Kept outside the environment stacks on purpose:
# - the OIDC provider is one per account and shared with Unify Data and the
#   UnifyAPI web deploy; it must not belong to either environment's state;
# - the role ARN goes into GitHub environment settings once, and should not
#   change because staging was destroyed and rebuilt.
# The roles find their instance by tag, which is why they need nothing from
# the environment states.

data "aws_caller_identity" "current" {}

# The provider existed on 2026-10-02 (created 2026-08-01 for Unify Data). Check
# with:
#
#     aws iam list-open-id-connect-providers
#
# and set create_oidc_provider = true only if that list has no
# token.actions.githubusercontent.com entry.
data "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 0 : 1
  url   = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  # AWS no longer uses the thumbprint for this issuer, but the API still
  # requires one; this is the value already on the account's provider.
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

locals {
  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : data.aws_iam_openid_connect_provider.github[0].arn
  account_id        = data.aws_caller_identity.current.account_id
}

data "aws_iam_policy_document" "trust" {
  for_each = toset(var.environments)

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # StringEquals on the full subject, not StringLike on a prefix: only a job
    # that declares `environment: <env>` gets this role, so the GitHub
    # environment's protection rules (reviewers, branch policy) gate it.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${var.github_subject_prefix}:environment:${each.key}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  for_each = toset(var.environments)

  name                 = "unify-compute-${each.key}-deploy"
  description          = "GitHub Actions deploys to the unify-compute ${each.key} instance over SSM"
  assume_role_policy   = data.aws_iam_policy_document.trust[each.key].json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "deploy" {
  for_each = toset(var.environments)

  # SendCommand authorises against both the target and the document, and the
  # tag condition only makes sense on the instance, hence two statements.
  statement {
    sid       = "RunCommandsOnThisEnvironmentsInstanceOnly"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ec2:${var.region}:${local.account_id}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/Project"
      values   = ["unify-compute"]
    }

    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/Environment"
      values   = [each.key]
    }
  }

  statement {
    sid       = "WithTheShellDocumentOnly"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ssm:${var.region}::document/AWS-RunShellScript"]
  }

  # None of these accept resource-level scoping. They read command results and
  # find the instance by its tags; they cannot change anything.
  statement {
    sid = "ReadBackTheResultAndFindTheInstance"
    actions = [
      "ssm:GetCommandInvocation",
      "ssm:ListCommandInvocations",
      "ssm:ListCommands",
      "ec2:DescribeInstances",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "deploy" {
  for_each = toset(var.environments)

  name   = "deploy"
  role   = aws_iam_role.deploy[each.key].id
  policy = data.aws_iam_policy_document.deploy[each.key].json
}
