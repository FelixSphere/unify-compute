# One Graviton instance running deploy/compose from this repository, a data
# volume that outlives it, and an IAM role that can read this stack's SSM
# parameters and accept SSM sessions. No SSH.

data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

locals {
  ami_id = var.ami_id != "" ? var.ami_id : data.aws_ssm_parameter.al2023_arm64.value

  # One prefix for the parameter names and the IAM policy that reads them.
  # Renaming the stack without moving the parameters (or vice versa) used to
  # fail only at boot, as AccessDenied inside user-data.
  ssm_prefix            = "/${var.name}"
  ssm_postgres_password = coalesce(var.ssm_postgres_password, "${local.ssm_prefix}/postgres_password")
  ssm_redis_password    = coalesce(var.ssm_redis_password, "${local.ssm_prefix}/redis_password")
  ssm_session_secret    = coalesce(var.ssm_session_secret, "${local.ssm_prefix}/session_secret")
  ssm_bridge_secret     = coalesce(var.ssm_bridge_secret, "${local.ssm_prefix}/bridge_secret")
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

data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "secrets" {
  statement {
    actions   = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter${local.ssm_prefix}/*"]
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
  tags              = { Name = "${var.name}-data" }

  # Both databases live here. A plan that proposes destroying this is a plan
  # that destroys the environment; stop and read it.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_instance" "this" {
  ami                    = local.ami_id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public[0].id
  vpc_security_group_ids = [aws_security_group.instance.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  # A user_data change replaces the instance. Without this, terraform updates
  # the attribute in place, nothing re-runs, and the apply reports success
  # while deploying nothing. The data volume survives; the containers do not.
  user_data_replace_on_change = true

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    name                  = var.name
    region                = var.region
    environment           = var.environment
    volume_id             = aws_ebs_volume.data.id
    compose_file          = file("${path.module}/../../compose/docker-compose.yml")
    postgres_init         = file("${path.module}/../../compose/postgres-init.sql")
    console_image         = var.console_image
    compute_api_image     = var.compute_api_image
    console_domain        = var.console_domain
    ssm_postgres_password = local.ssm_postgres_password
    ssm_redis_password    = local.ssm_redis_password
    ssm_session_secret    = local.ssm_session_secret
    ssm_bridge_secret     = local.ssm_bridge_secret
  })

  root_block_device {
    volume_size = 16
    volume_type = "gp3"
    encrypted   = true
  }

  metadata_options {
    http_tokens = "required" # IMDSv2 only
  }

  tags = { Name = var.name }
}

resource "aws_volume_attachment" "data" {
  # Instance replacement (AMI drift, any user_data edit) destroys this first,
  # which detaches a mounted XFS volume from a live instance and can hang.
  # Stop unify-compute.service over SSM before applying a replacement.
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.this.id
}
