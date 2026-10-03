output "budget_name" {
  value = aws_budgets_budget.gpu_workers.name
}

output "stop_launches_policy_arn" {
  description = "Attached to the launch roles when the month's budget is spent. Detached by reversing the budget action (main.tf)."
  value       = aws_iam_policy.stop_launches.arn
}

output "budget_action_id" {
  description = "For aws budgets execute-budget-action --execution-type REVERSE_BUDGET_ACTION."
  value       = aws_budgets_budget_action.stop_launches.action_id
}
