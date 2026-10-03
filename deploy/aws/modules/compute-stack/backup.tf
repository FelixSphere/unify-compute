# Off-host backups of the data volume: daily EBS snapshots through Data
# Lifecycle Manager.
#
# Unify Data keeps its backups on the same disk as the data, so losing the
# volume loses both. Snapshots are stored by EBS outside the volume and the
# instance, survive both being destroyed, and are restored by creating a new
# volume from one (docs/staging.md, "Restoring the data volume").
#
# Why snapshots and not a nightly pg_dump to S3:
# - Nothing runs on the host. A pg_dump timer is a script, an IAM write
#   permission and a failure mode (a dump that silently stopped) that someone
#   has to monitor; DLM failures are AWS-side and visible in the console.
# - One volume holds the whole Postgres data directory including WAL, so an
#   EBS snapshot is crash-consistent, which is exactly what Postgres recovers
#   from after a power loss. It also covers SkyPilot state on the same volume.
# - What it does not give: a portable, per-database logical dump, or a copy
#   outside this account and region. If either becomes a requirement, add a
#   pg_dump job then; the snapshot schedule stays as the first line.

data "aws_iam_policy_document" "dlm_assume" {
  count = var.snapshot_backups ? 1 : 0

  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dlm" {
  count = var.snapshot_backups ? 1 : 0

  name               = "${var.name}-dlm"
  assume_role_policy = data.aws_iam_policy_document.dlm_assume[0].json
}

resource "aws_iam_role_policy_attachment" "dlm" {
  count = var.snapshot_backups ? 1 : 0

  role       = aws_iam_role.dlm[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "data" {
  count = var.snapshot_backups ? 1 : 0

  description        = "${var.name} data volume daily" # DLM allows [0-9A-Za-z _-] only
  execution_role_arn = aws_iam_role.dlm[0].arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Backup = "${var.name}-daily" }

    schedule {
      name      = "daily"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["05:00"] # UTC; the quietest hour for a US-facing product
      }

      retain_rule {
        count = var.snapshot_retain_count
      }

      tags_to_add = { SnapshotOf = "${var.name}-data" }
    }
  }
}

# ---------------------------------------------------------------------------
# Pre-release dumps.
#
# The release pipeline (scripts/release/host.sh) takes a pg_dump of every
# database immediately before it swaps images, because compute-api migrates
# at startup and a snapshot cannot be restored per database in seconds; a
# dump can. It reads the bucket name from SSM /<name>/backup_bucket, refuses
# to release without it, and writes s3://<bucket>/<env>/releases/<stamp>/.
#
# These dumps are a release's undo, not the backup schedule: 30 days, no
# versioning, and the instance can only add objects (instance.tf).
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "backups" {
  bucket        = "${var.name}-backups-${data.aws_caller_identity.current.account_id}"
  force_destroy = var.buckets_force_destroy
  tags          = { Name = "${var.name}-backups" }
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    id     = "expire"
    status = "Enabled"
    filter {}

    expiration {
      days = 30
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

# A bucket name is not a secret, so unlike the passwords this is a Terraform
# resource: its value in state is harmless, and the name stays in step with
# the bucket.
resource "aws_ssm_parameter" "backup_bucket" {
  name  = "${local.ssm_prefix}/backup_bucket"
  type  = "String"
  value = aws_s3_bucket.backups.id
}
