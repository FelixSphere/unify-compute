# One budget over every GPU worker in the account, both environments.
#
# Its own stack, like ecr and github-oidc, because a budget filters on a tag
# value and cannot AND two tags: Project=unify-compute-workers is shared by
# staging's and production's workers, so a budget in either environment's
# stack would count the other's. Cost Explorer splits it by the Environment
# tag when someone needs to know whose it was.
#
# Prerequisite, account-wide and therefore not managed here: the Project tag
# must be an active cost allocation tag. It was Inactive on 2026-10-03, so
# until it is activated this budget sees nothing. Activation is not
# retroactive (only costs incurred afterwards carry the tag) and can take up
# to 24 hours to show:
#
#     aws ce update-cost-allocation-tags-status \
#       --cost-allocation-tags-status TagKey=Project,Status=Active
#
# It is left out of Terraform on purpose: every product in the account tags
# with Project, and destroying this stack must not switch their cost
# reporting off. Until it is active, the per-environment reaper
# (modules/gpu-workers) is the only spend control that works, and the budget
# action below never fires.
#
# What this stack enforces, not just reports (ADR-0010, "Start small"):
# at 100% of the month's actual spend, AWS Budgets attaches an explicit Deny
# on ec2:RunInstances and ec2:StartInstances to the SkyPilot launch role(s),
# with no approval step. Running workers are left alone (compute-api's /down
# and the reaper still terminate them; the Deny does not cover
# TerminateInstances or StopInstances), and nothing new launches until the
# operator lifts it or the month ends.
#
# Lifting it, in the month it fired:
#   - Raise the budget (monthly_limit_usd, applied here), then reverse the
#     action so the policy is detached:
#       aws budgets execute-budget-action --account-id <id> \
#         --budget-name unify-compute-gpu-workers --action-id <id> \
#         --execution-type REVERSE_BUDGET_ACTION
#     A reversed action is not evaluated again until the next month; use
#     --execution-type RESET_BUDGET_ACTION afterwards to re-arm it now.
#   - Or detach unify-compute-gpu-budget-stop-launches from the role in IAM.
#     Prefer the reverse: the action stays "completed" otherwise, and the
#     console shows a control that looks applied but is not.
# It resets by itself: IAM-policy actions return to standby at the start of
# each budget period and the policy is detached then.
#
# Budget data lags actual spend by roughly 8 to 12 hours (AWS refreshes it
# about three times a day), so spend overshoots the limit before the Deny
# lands. With one worker at a time (compute-api/config/platform.json and the
# 4-vCPU quota) the bound is in ADR-0010: about USD 1.04/h x (lag + the
# reaper's 5 h).

resource "aws_budgets_budget" "gpu_workers" {
  name         = "unify-compute-gpu-workers"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_limit_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  cost_filter {
    name   = "TagKeyValue"
    values = ["user:Project$unify-compute-workers"]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 50
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }

  # The same moment the action below fires: says so to a person as well.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }

  # Forecast: the warning that arrives while there is still a month to act in.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}

data "aws_caller_identity" "current" {}

locals {
  launch_role_arns = [for r in var.launch_role_names : "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${r}"]
}

# What the action attaches. A managed policy because Budgets attaches a
# policy by ARN; it does nothing until attached. Stop and Terminate are left
# out on purpose: teardown must keep working while launches are stopped.
data "aws_iam_policy_document" "stop_launches" {
  statement {
    sid       = "GpuBudgetSpentNoNewWorkers"
    effect    = "Deny"
    actions   = ["ec2:RunInstances", "ec2:StartInstances"]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "stop_launches" {
  name        = "unify-compute-gpu-budget-stop-launches"
  description = "Attached by the unify-compute-gpu-workers budget action when the month's GPU budget is spent: no new or restarted GPU workers. Detached by reversing the action, or at the start of the next month."
  policy      = data.aws_iam_policy_document.stop_launches.json
}

# The identity AWS Budgets acts as. It can attach and detach that one
# policy, to and from the launch roles, and nothing else: not another
# policy (an AdministratorAccess attach would otherwise be one API call
# away), not another role.
data "aws_iam_policy_document" "budget_action_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:budgets::${data.aws_caller_identity.current.account_id}:budget/${aws_budgets_budget.gpu_workers.name}*"]
    }
  }
}

resource "aws_iam_role" "budget_action" {
  name               = "unify-compute-gpu-budget-action"
  description        = "Assumed by AWS Budgets to attach or detach unify-compute-gpu-budget-stop-launches on the SkyPilot launch roles, and nothing else."
  assume_role_policy = data.aws_iam_policy_document.budget_action_trust.json
}

data "aws_iam_policy_document" "budget_action" {
  statement {
    sid       = "AttachOnlyTheStopPolicyToOnlyTheLaunchRoles"
    actions   = ["iam:AttachRolePolicy", "iam:DetachRolePolicy"]
    resources = local.launch_role_arns

    condition {
      test     = "ArnEquals"
      variable = "iam:PolicyARN"
      values   = [aws_iam_policy.stop_launches.arn]
    }
  }
}

resource "aws_iam_role_policy" "budget_action" {
  name   = "apply-gpu-budget-stop"
  role   = aws_iam_role.budget_action.id
  policy = data.aws_iam_policy_document.budget_action.json
}

resource "aws_budgets_budget_action" "stop_launches" {
  budget_name        = aws_budgets_budget.gpu_workers.name
  action_type        = "APPLY_IAM_POLICY"
  approval_model     = "AUTOMATIC"
  notification_type  = "ACTUAL"
  execution_role_arn = aws_iam_role.budget_action.arn

  action_threshold {
    action_threshold_type  = "PERCENTAGE"
    action_threshold_value = 100
  }

  definition {
    iam_action_definition {
      policy_arn = aws_iam_policy.stop_launches.arn
      roles      = var.launch_role_names
    }
  }

  subscriber {
    address           = var.alert_email
    subscription_type = "EMAIL"
  }

  # The role's permissions must exist before Budgets can use it.
  depends_on = [aws_iam_role_policy.budget_action]
}
