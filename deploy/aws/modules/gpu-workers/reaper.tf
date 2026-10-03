# The orphan reaper: a scheduled Lambda, deliberately not on the compute host.
# The cases it exists for (host down, SkyPilot state lost, compute-api not
# running past a job's cap: backend.md open question 3) are the cases in which
# anything running on the host is not running either.

locals {
  reaper_name = "${var.name}-gpu-reaper"

  # The platform's ceiling on max_runtime_seconds (upstreams.md R9) is read
  # from the file compute-api enforces it from, so the reaper can never be
  # left at an older, longer ceiling when the platform's changes (ADR-0010,
  # "Start small": 4 h, so workers are reaped at 5 h).
  max_runtime_hours = jsondecode(file("${path.module}/../../../../compute-api/config/platform.json")).max_runtime_seconds / 3600
  reaper_max_age    = local.max_runtime_hours + var.reaper_margin_hours
}

data "archive_file" "reaper" {
  type        = "zip"
  source_file = "${path.module}/reaper/reaper.py"
  output_path = "${path.root}/.terraform/${local.reaper_name}.zip"
}

resource "aws_sns_topic" "reaper" {
  name = local.reaper_name
}

resource "aws_sns_topic_subscription" "reaper_email" {
  count = var.alert_email == null ? 0 : 1

  topic_arn = aws_sns_topic.reaper.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

resource "aws_cloudwatch_log_group" "reaper" {
  name              = "/aws/lambda/${local.reaper_name}"
  retention_in_days = 90
}

data "aws_iam_policy_document" "reaper_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "reaper" {
  name               = local.reaper_name
  assume_role_policy = data.aws_iam_policy_document.reaper_trust.json
}

data "aws_iam_policy_document" "reaper" {
  statement {
    sid       = "FindWorkers"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"]
  }

  # Same tag boundary as the launch role: it cannot terminate anything that
  # is not this environment's worker.
  statement {
    sid       = "TerminateOnlyThisEnvironmentsWorkers"
    actions   = ["ec2:TerminateInstances"]
    resources = [local.instance_arn]

    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/Project"
      values   = [local.project]
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/Environment"
      values   = [var.environment]
    }
  }

  statement {
    sid       = "Report"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.reaper.arn]
  }

  statement {
    sid       = "Log"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.reaper.arn}:*"]
  }
}

resource "aws_iam_role_policy" "reaper" {
  name   = "reap-gpu-workers"
  role   = aws_iam_role.reaper.id
  policy = data.aws_iam_policy_document.reaper.json
}

resource "aws_lambda_function" "reaper" {
  function_name    = local.reaper_name
  description      = "Terminates ${var.environment} GPU workers older than ${local.reaper_max_age}h and reports"
  role             = aws_iam_role.reaper.arn
  runtime          = "python3.12"
  handler          = "reaper.handler"
  filename         = data.archive_file.reaper.output_path
  source_code_hash = data.archive_file.reaper.output_base64sha256
  timeout          = 60
  memory_size      = 128
  architectures    = ["arm64"]

  environment {
    variables = {
      PROJECT        = local.project
      ENVIRONMENT    = var.environment
      MAX_AGE_HOURS  = tostring(local.reaper_max_age)
      MAX_CONCURRENT = tostring(var.max_concurrent_workers)
      TOPIC_ARN      = aws_sns_topic.reaper.arn
    }
  }

  depends_on = [aws_cloudwatch_log_group.reaper]
}

# Every 15 minutes: a reaped worker overstays by at most that, ~USD 0.25 on a
# g5.xlarge, and the concurrency report is never more than 15 minutes late.
resource "aws_cloudwatch_event_rule" "reaper" {
  name                = local.reaper_name
  description         = "Run the ${var.environment} GPU worker reaper"
  schedule_expression = "rate(15 minutes)"
}

resource "aws_cloudwatch_event_target" "reaper" {
  rule = aws_cloudwatch_event_rule.reaper.name
  arn  = aws_lambda_function.reaper.arn
}

resource "aws_lambda_permission" "reaper" {
  statement_id  = "AllowEventBridgeSchedule"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.reaper.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.reaper.arn
}

# A reaper that stopped running is silent. Errors and a missing invocation
# both alarm to the same topic.
resource "aws_cloudwatch_metric_alarm" "reaper_errors" {
  alarm_name          = "${local.reaper_name}-errors"
  alarm_description   = "The ${var.environment} GPU reaper failed or did not run; orphaned workers would not be terminated."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.reaper.function_name }
  statistic           = "Sum"
  period              = 900
  evaluation_periods  = 2
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.reaper.arn]
}

resource "aws_cloudwatch_metric_alarm" "reaper_not_running" {
  alarm_name          = "${local.reaper_name}-not-running"
  alarm_description   = "The ${var.environment} GPU reaper has not been invoked for an hour."
  namespace           = "AWS/Lambda"
  metric_name         = "Invocations"
  dimensions          = { FunctionName = aws_lambda_function.reaper.function_name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.reaper.arn]
}
