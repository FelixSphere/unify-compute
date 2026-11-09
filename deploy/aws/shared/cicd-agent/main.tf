# The AWS identity of the unify-compute CI/CD agent (.claude/agents/cicd.md)
# when it runs in a Claude Code cloud session.
#
# Why it exists: releases themselves go through GitHub Actions and the OIDC
# roles in deploy/aws/shared/github-oidc, which need no key. But the agent also
# exercises a release on the host, and some checks need a shell there: a
# throwaway account's sign-up code is read from its `email_codes` row over SSM
# (the 2026-10-14 to 2026-10-17 staging records). On 2026-11-09 the session had
# only a proxy placeholder key, AWS answered InvalidClientTokenId, and a
# staging check for #145 went unexercised. A placeholder the proxy substitutes
# cannot work for AWS at all: SigV4 signs the request locally with the secret,
# so the real key has to be in the process environment.
#
# Why a user and not a role: the cloud session has nothing to assume a role
# with. The cost is a long-lived key, so the policy is what bounds it.
#
# What it can do: send AWS-RunShellScript to the instances named in
# var.environments, and read back the result. That is a root shell on those
# hosts, which is what the checks need and why production is not in the
# default. It cannot touch IAM, Terraform state, S3, ECR or any other host.
# It has no Terraform plan access; that would need read access across the
# account, including the backup buckets, and is a separate decision.
#
# Apply, then create the key by hand so the secret never enters state (an
# aws_iam_access_key resource would write it there):
#
#     terraform -chdir=deploy/aws/shared/cicd-agent init
#     terraform -chdir=deploy/aws/shared/cicd-agent apply
#     aws iam create-access-key --user-name unify-compute-cicd-agent
#
# Put the two values in the cloud environment's settings as ordinary
# environment variables AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, not as
# network secrets, and remove the old AWS network secret. Start a new session;
# `aws sts get-caller-identity` should report output.user_arn. Rotate by
# creating a second key, swapping it in, then deleting the first
# (`aws iam delete-access-key`).

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
}

resource "aws_iam_user" "agent" {
  name = "unify-compute-cicd-agent"
  # Kept apart from human and other-product users, so a list of what to rotate
  # or revoke for this product is one prefix.
  path = "/unify-compute/"
}

data "aws_iam_policy_document" "agent" {
  # SendCommand authorises against both the target and the document, and the
  # tag condition only makes sense on the instance, hence two statements. Same
  # scoping as the GitHub deploy roles.
  statement {
    sid       = "RunCommandsOnTheNamedInstancesOnly"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ec2:${var.region}:${local.account_id}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/Name"
      values   = [for e in var.environments : "unify-compute-${e}"]
    }

    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/Project"
      values   = ["unify-compute"]
    }
  }

  statement {
    sid       = "WithTheShellDocumentOnly"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ssm:${var.region}::document/AWS-RunShellScript"]
  }

  # None of these accept resource-level scoping. They read command results,
  # find the instance by its tags and confirm the key works; they change nothing.
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

resource "aws_iam_user_policy" "agent" {
  name   = "cicd-agent"
  user   = aws_iam_user.agent.name
  policy = data.aws_iam_policy_document.agent.json
}
