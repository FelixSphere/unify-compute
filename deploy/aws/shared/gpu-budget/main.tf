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
# (modules/gpu-workers) is the only spend control that works.

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

  # Forecast: the warning that arrives while there is still a month to act in.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}
