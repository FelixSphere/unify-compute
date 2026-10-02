# ---------------------------------------------------------------------------
# ALB access logs.
#
# UnifyAPI production runs with ALB access logs OFF, which is the first blind
# spot its SRE agent lists: there is no per-request record at the edge, so
# questions about who called what, and how often, have no source. Container
# logs never leave that box either.
#
# Turning this on costs one bucket and is free to do before the first apply.
# Retrofitting it means a listener-adjacent change to a live load balancer.
# ---------------------------------------------------------------------------

data "aws_elb_service_account" "this" {}

resource "aws_s3_bucket" "alb_logs" {
  bucket = "${var.name}-alb-logs-${data.aws_caller_identity.current.account_id}"

  # Staging passes true: it is meant to be destroyable and its logs are
  # diagnostic. Production passes false, so a destroy stops at a non-empty
  # bucket instead of deleting the access record with it.
  force_destroy = var.alb_logs_force_destroy

  tags = { Name = "${var.name}-alb-logs" }
}

resource "aws_s3_bucket_public_access_block" "alb_logs" {
  bucket                  = aws_s3_bucket.alb_logs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id

  rule {
    # ALB access-log delivery supports SSE-S3 only; SSE-KMS silently drops
    # deliveries rather than failing the apply.
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id

  rule {
    id     = "expire"
    status = "Enabled"
    filter {}

    expiration {
      days = var.alb_log_retention_days
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

data "aws_iam_policy_document" "alb_logs" {
  # us-east-1 is an older ELB region: delivery comes from the regional ELB
  # account, not from the logdelivery service principal used by newer regions.
  statement {
    principals {
      type        = "AWS"
      identifiers = [data.aws_elb_service_account.this.arn]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.alb_logs.arn}/*"]
  }

  statement {
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.alb_logs.arn}/*"]
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }

  statement {
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.alb_logs.arn]
  }
}

resource "aws_s3_bucket_policy" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  policy = data.aws_iam_policy_document.alb_logs.json
}
