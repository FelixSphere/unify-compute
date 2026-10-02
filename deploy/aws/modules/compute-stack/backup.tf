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
