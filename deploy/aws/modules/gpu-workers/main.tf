data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id

  # The tag every worker carries and every mutating permission below is
  # conditioned on. Distinct from the compute stack's own Project tag
  # (unify-compute), which the GitHub deploy roles match on: a worker can
  # never be mistaken for, or retagged into, the host.
  project = "unify-compute-workers"

  workers_name     = "${var.name}-workers"
  launch_role_name = "${var.name}-skypilot"
  worker_role_name = "${var.name}-worker"

  # Built from names rather than read from the resources, so the compute
  # host's user-data and SkyPilot config (which embed these) are known at plan
  # time and do not depend on this module's resources existing.
  launch_role_arn    = "arn:aws:iam::${local.account_id}:role/${local.launch_role_name}"
  worker_profile_arn = "arn:aws:iam::${local.account_id}:instance-profile/${local.worker_role_name}"

  # ~/.sky/config.yaml for the API server (SkyPilot v0.13.0 schema,
  # sky/utils/schemas.py). Every key here closes a default that would
  # otherwise be wrong in this account:
  # - vpc_names: without it SkyPilot uses the default VPC (172.31/16).
  # - security_group_name: without it SkyPilot creates sky-sg-* with SSH open
  #   to the world.
  # - remote_identity: without it (LOCAL_CREDENTIALS, or NO_UPLOAD, which
  #   falls back to it) SkyPilot creates and attaches its own role,
  #   skypilot-v1, with AmazonEC2FullAccess and AmazonS3FullAccess
  #   (sky/provision/aws/config.py, _configure_iam_role). Customer code on a
  #   worker would then hold the right to terminate every instance in the
  #   account. Here it gets a role that is denied everything.
  # - labels: the tags the launch role requires on every instance and volume.
  # - allowed_clouds: nothing but AWS is configured; nothing else is checked.
  skypilot_config = yamlencode({
    allowed_clouds = ["aws"]
    aws = {
      vpc_names           = [local.workers_name]
      security_group_name = local.workers_name
      remote_identity     = local.worker_role_name
      use_internal_ips    = false
      disk_encrypted      = true
      labels = {
        Project     = local.project
        Environment = var.environment
      }
    }
  })
}
