# One Graviton instance running deploy/compose from this repository, a data
# volume that outlives it, and an IAM role that can read this stack's SSM
# parameters and accept SSM sessions. No SSH.

data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

data "aws_caller_identity" "current" {}

locals {
  # The SSM data source marks every value sensitive, including this public
  # AMI ID. Left that way, the ami_id_in_use output fails the plan ("Output
  # refers to sensitive values") — which the old root stack would have hit on
  # its first real plan, since `validate` never evaluates it.
  ami_id          = var.ami_id != "" ? var.ami_id : nonsensitive(data.aws_ssm_parameter.al2023_arm64.value)
  console_enabled = var.console_domain != null

  # Host ports. compute-api and the site both listen on 8080 inside their
  # containers; the site is published on 8081 so the two do not collide.
  apps = merge(
    {
      api  = { port = 8080, health = "/healthz", domain = var.api_domain, priority = 10, aliases = [] }
      site = { port = 8081, health = "/healthz", domain = var.site_domain, priority = 20, aliases = var.site_aliases }
    },
    local.console_enabled ? {
      console = { port = 3000, health = "/api/status", domain = var.console_domain, priority = 30, aliases = [] }
    } : {}
  )

  # One prefix for the parameter names and the IAM policy that reads them.
  # Renaming the stack without moving the parameters (or vice versa) used to
  # fail only at boot, as AccessDenied inside user-data.
  ssm_prefix            = "/${var.name}"
  ssm_postgres_password = coalesce(var.ssm_postgres_password, "${local.ssm_prefix}/postgres_password")
  ssm_redis_password    = coalesce(var.ssm_redis_password, "${local.ssm_prefix}/redis_password")
  ssm_session_secret    = coalesce(var.ssm_session_secret, "${local.ssm_prefix}/session_secret")
  ssm_bridge_secret     = coalesce(var.ssm_bridge_secret, "${local.ssm_prefix}/bridge_secret")

  compose_dir = "${path.module}/../../../compose"
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = var.name
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "secrets" {
  statement {
    actions   = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter${local.ssm_prefix}/*"]
  }

  # The release pipeline's pre-release pg_dump (backup.tf). Put only: the
  # instance can add dumps, not read or delete the ones already there.
  statement {
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.backups.arn}/*"]
  }

  statement {
    actions   = ["kms:Decrypt"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "secrets" {
  name   = "${var.name}-secrets"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.secrets.json
}

resource "aws_iam_instance_profile" "instance" {
  name = var.name
  role = aws_iam_role.instance.name
}

resource "aws_ebs_volume" "data" {
  # The AZ comes from the availability_zones data source ordering. If that
  # ordering ever changes, subnet[0] moves AZ and this volume would have to
  # move with it; prevent_destroy turns that plan into an error on purpose.
  availability_zone = aws_subnet.public[0].availability_zone
  size              = var.data_volume_gb
  type              = "gp3"
  encrypted         = true

  # Backup is the tag the DLM policy in backup.tf targets. It is set in both
  # environments so turning snapshots on is one variable, not a retag.
  tags = { Name = "${var.name}-data", Backup = "${var.name}-daily" }

  # Every database lives here. A plan that proposes destroying this is a plan
  # that destroys the environment; stop and read it.
  lifecycle {
    prevent_destroy = true
  }
}

# Rendered once so the precondition below can measure it.
locals {
  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    name                  = var.name
    ssm_prefix            = local.ssm_prefix
    region                = var.region
    environment           = var.environment
    volume_id             = aws_ebs_volume.data.id
    compose_file          = file("${local.compose_dir}/docker-compose.yml")
    compose_console_file  = file("${local.compose_dir}/docker-compose.console.yml")
    postgres_init_console = file("${local.compose_dir}/postgres-init-console.sql")
    console_enabled       = local.console_enabled
    bridge_enabled        = var.bridge_enabled
    console_image         = local.console_enabled ? var.console_image : ""
    console_domain        = local.console_enabled ? var.console_domain : ""
    console_url           = var.console_url
    compute_api_image     = var.compute_api_image
    site_image            = var.site_image
    ssm_postgres_password = local.ssm_postgres_password
    ssm_redis_password    = local.ssm_redis_password
    ssm_session_secret    = local.ssm_session_secret
    ssm_bridge_secret     = local.ssm_bridge_secret
  })
}

resource "aws_instance" "this" {
  ami                    = local.ami_id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public[0].id
  vpc_security_group_ids = [aws_security_group.instance.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  # user_data is ignored after creation (lifecycle below), so this setting
  # only decides what an explicit `-replace` does with it: render it fresh.
  user_data_replace_on_change = true

  user_data = local.user_data

  root_block_device {
    volume_size = 16
    volume_type = "gp3"
    encrypted   = true
  }

  metadata_options {
    http_tokens = "required" # IMDSv2 only
  }

  # Name and Project are what the GitHub deploy roles match on
  # (ssm:resourceTag in deploy/aws/shared/github-oidc), and Name is how the
  # release pipeline finds the instance. They are set here rather than left
  # to the root's default_tags so that a caller with different default tags
  # cannot silently lock its own deploys out.
  tags = {
    Name        = var.name
    Project     = "unify-compute"
    Environment = var.environment
  }

  lifecycle {
    # user_data is a first-boot script, and the image pins are in it. Neither
    # way of letting Terraform act on a change is acceptable for production:
    # - replace_on_change = true makes every pin bump a new instance;
    # - replace_on_change = false still makes EC2 stop and start the instance
    #   to write the attribute (the UnifyAPI console stack found this out):
    #   minutes of 502s, and nothing re-runs the script anyway.
    # So pins here are only the rebuild baseline. Releases swap images on the
    # running host over SSM (scripts/release/host.sh). A deliberate rebuild —
    # to apply a user-data change, or move the baseline — is:
    #   1. set the pins here to what is currently released (deploy/releases/);
    #   2. stop unify-compute.service over SSM (the volume detach can hang on a
    #      mounted filesystem otherwise);
    #   3. terraform apply -replace='module.stack.aws_instance.this'
    # and in production only after the same replace has been run on staging.
    ignore_changes = [user_data]

    precondition {
      condition     = var.name == "unify-compute-${var.environment}"
      error_message = "name must be unify-compute-<environment>: the deploy roles and the release pipeline find the instance by that Name tag, and the SSM prefix is derived from it."
    }

    precondition {
      condition     = !local.console_enabled || var.console_image != null
      error_message = "console_domain is set, so console_image must be too."
    }

    # EC2 rejects user data over 16 KB, but only at apply, after the plan
    # looked fine. The compose files are embedded verbatim, so growing them
    # grows this; staging rendered at ~12 KB on 2026-10-02.
    precondition {
      condition     = length(local.user_data) <= 16384
      error_message = "Rendered user_data exceeds EC2's 16 KB limit; trim the template or the embedded compose files."
    }
  }
}

resource "aws_volume_attachment" "data" {
  # Instance replacement (AMI drift, any user_data edit) destroys this first,
  # which detaches a mounted XFS volume from a live instance and can hang.
  # Stop unify-compute.service over SSM before applying a replacement.
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.this.id
}

# A stable public address. A stop/start (an instance-type change, an AWS
# maintenance event, a hypervisor retirement) otherwise gives the instance a
# new auto-assigned IPv4, which breaks anything that allowlists our egress —
# such as the console bridge's callers, should app.unifyapi.ai ever restrict
# them. It costs nothing extra: AWS bills every public IPv4 in use the same
# $0.005/h, and this one replaces the auto-assigned address, so the count in
# ADR-0009's estimate is unchanged.
resource "aws_eip" "instance" {
  domain = "vpc"
  tags   = { Name = var.name }
}

resource "aws_eip_association" "instance" {
  allocation_id = aws_eip.instance.id
  instance_id   = aws_instance.this.id
}
