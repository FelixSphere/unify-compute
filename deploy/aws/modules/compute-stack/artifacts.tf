# ---------------------------------------------------------------------------
# Job artifacts (ADR-0005).
#
# One bucket per environment. Outputs live under jobs/<job_id>/ and leave
# only through presigned URLs that compute-api signs after an owner-scoped
# lookup. Nothing mounts this bucket, and no job node holds credentials for
# it: a node gets one presigned PUT per file, signed by a session of the
# artifacts-writer role below whose inline session policy names that job's
# prefix alone.
#
# Retention is 30 days from upload, which is the end of the job's run step.
# Server access logs go to their own bucket because the egress meter
# (docs/spec/metering.md) is built from their Bytes Sent field.
# ---------------------------------------------------------------------------

locals {
  artifacts_bucket      = "${var.name}-artifacts-${data.aws_caller_identity.current.account_id}"
  artifacts_logs_bucket = "${var.name}-artifacts-logs-${data.aws_caller_identity.current.account_id}"
}

resource "aws_s3_bucket" "artifacts" {
  bucket        = local.artifacts_bucket
  force_destroy = var.buckets_force_destroy
  tags          = { Name = "${var.name}-artifacts" }
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    id     = "jobs-retention"
    status = "Enabled"
    filter {
      prefix = "jobs/"
    }

    expiration {
      days = var.artifacts_retention_days
    }

    # The run step uses single PUTs today; this is for when multipart lands,
    # so an abandoned upload is not stored (and paid for) forever.
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

data "aws_iam_policy_document" "artifacts_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.artifacts.arn, "${aws_s3_bucket.artifacts.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  policy = data.aws_iam_policy_document.artifacts_bucket.json

  depends_on = [aws_s3_bucket_public_access_block.artifacts]
}

resource "aws_s3_bucket_logging" "artifacts" {
  bucket        = aws_s3_bucket.artifacts.id
  target_bucket = aws_s3_bucket.artifacts_logs.id
  target_prefix = "s3/"

  # Date-partitioned keys (s3/<account>/<region>/<bucket>/YYYY/MM/DD/...), so
  # the daily egress collector lists one day instead of the whole bucket.
  target_object_key_format {
    partitioned_prefix {
      partition_date_source = "EventTime"
    }
  }

  depends_on = [aws_s3_bucket_policy.artifacts_logs]
}

# --- Server access logs -----------------------------------------------------

resource "aws_s3_bucket" "artifacts_logs" {
  bucket        = local.artifacts_logs_bucket
  force_destroy = var.buckets_force_destroy
  tags          = { Name = "${var.name}-artifacts-logs" }
}

resource "aws_s3_bucket_public_access_block" "artifacts_logs" {
  bucket                  = aws_s3_bucket.artifacts_logs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# With BucketOwnerEnforced, delivery is granted by the bucket policy below,
# not by the log-delivery ACL.
resource "aws_s3_bucket_ownership_controls" "artifacts_logs" {
  bucket = aws_s3_bucket.artifacts_logs.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts_logs" {
  bucket = aws_s3_bucket.artifacts_logs.id

  rule {
    # Server access log delivery supports SSE-S3 on the target bucket.
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts_logs" {
  bucket = aws_s3_bucket.artifacts_logs.id

  rule {
    id     = "expire"
    status = "Enabled"
    filter {}

    expiration {
      days = var.artifacts_log_retention_days
    }
  }
}

data "aws_iam_policy_document" "artifacts_logs" {
  statement {
    sid       = "S3ServerAccessLogsPolicy"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.artifacts_logs.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["logging.s3.amazonaws.com"]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [aws_s3_bucket.artifacts.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts_logs" {
  bucket = aws_s3_bucket.artifacts_logs.id
  policy = data.aws_iam_policy_document.artifacts_logs.json

  depends_on = [aws_s3_bucket_public_access_block.artifacts_logs]
}

# --- The writer role --------------------------------------------------------
#
# Assumed only by the instance role, per job, with an inline session policy
# that narrows it to s3://<bucket>/jobs/<job_id>/*. The session signs that
# job's upload URLs and its manifest write. From an instance role this is
# role chaining, which AWS caps at one hour whatever max_session_duration
# says; compute-api asks for exactly that.

data "aws_iam_policy_document" "artifacts_writer_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = [aws_iam_role.instance.arn]
    }
  }
}

resource "aws_iam_role" "artifacts_writer" {
  name                 = "${var.name}-artifacts-writer"
  assume_role_policy   = data.aws_iam_policy_document.artifacts_writer_trust.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "artifacts_writer" {
  statement {
    actions   = ["s3:PutObject", "s3:AbortMultipartUpload"]
    resources = ["${aws_s3_bucket.artifacts.arn}/jobs/*"]
  }
}

resource "aws_iam_role_policy" "artifacts_writer" {
  name   = "${var.name}-artifacts-writer"
  role   = aws_iam_role.artifacts_writer.id
  policy = data.aws_iam_policy_document.artifacts_writer.json
}

# --- What the instance role may do with artifacts ---------------------------
#
# Read and list for the manifest, the download URLs it presigns and the
# storage meter's ListObjectsV2; assume the writer. No PutObject or
# DeleteObject: every write goes through a job-scoped writer session.

data "aws_iam_policy_document" "instance_artifacts" {
  statement {
    actions   = ["sts:AssumeRole"]
    resources = [aws_iam_role.artifacts_writer.arn]
  }

  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.artifacts.arn}/jobs/*"]
  }

  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.artifacts.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["jobs/*"]
    }
  }
}

resource "aws_iam_role_policy" "instance_artifacts" {
  name   = "${var.name}-artifacts"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance_artifacts.json
}

# Names, not secrets, like backup_bucket: readable by the release pipeline
# and by a host that predates the .env lines user-data now writes.
resource "aws_ssm_parameter" "artifacts_bucket" {
  name  = "${local.ssm_prefix}/artifacts_bucket"
  type  = "String"
  value = aws_s3_bucket.artifacts.id
}

resource "aws_ssm_parameter" "artifacts_writer_role_arn" {
  name  = "${local.ssm_prefix}/artifacts_writer_role_arn"
  type  = "String"
  value = aws_iam_role.artifacts_writer.arn
}
