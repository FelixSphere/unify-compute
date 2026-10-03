# Two roles.
#
# launch (<name>-skypilot): what the SkyPilot API server acts as. Assumable
# only by the compute host's instance role. It can start the allow-listed
# instance types, only into this module's subnets and security group, only
# with this environment's worker tags, only with the worker instance profile,
# and it can stop, start, tag and terminate only instances that already carry
# those tags. Tag conditions are what make it impossible to touch another
# product's instances: none of them is tagged Project=unify-compute-workers,
# and the launch role cannot put that tag on an instance it did not create
# (CreateTags on an existing resource requires the tag to be there already).
# The Deny statements repeat the boundary so that it survives someone later
# attaching a broader policy to this role.
#
# worker (<name>-worker): what a worker instance runs as. Customer code shares
# the machine and can read IMDS, so this role is denied everything. Its only
# purpose is to exist, so SkyPilot attaches it instead of creating skypilot-v1
# (see locals in main.tf).
#
# Permissions are read from SkyPilot v0.13.0's documented minimal policy
# (docs/source/cloud-setup/cloud-permissions/aws.rst, "Minimal IAM policy") and
# narrowed by what its AWS provisioner actually calls
# (sky/provision/aws/{config,instance}.py): run/start/stop/terminate, tags on
# create and on the head node, Describe*. Left out on purpose:
# CreateSecurityGroup and AuthorizeSecurityGroupIngress (the group is
# pre-created), iam:CreateRole/AttachRolePolicy/CreateInstanceProfile (the
# worker profile is pre-created), DeleteTags (never called), and
# iam:CreateServiceLinkedRole for spot (AWSServiceRoleForEC2Spot already
# exists in the account).

data "aws_iam_policy_document" "launch_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = [var.api_server_role_arn]
    }
  }
}

resource "aws_iam_role" "launch" {
  name                 = local.launch_role_name
  description          = "SkyPilot API server on unify-compute-${var.environment}: launches and tears down GPU workers in ${local.workers_name} only"
  assume_role_policy   = data.aws_iam_policy_document.launch_trust.json
  max_session_duration = 3600 # role chaining from the instance role caps it at 1h anyway
}

locals {
  arn_prefix   = "arn:aws:ec2:${var.region}:${local.account_id}"
  instance_arn = "${local.arn_prefix}:instance/*"
  volume_arn   = "${local.arn_prefix}:volume/*"
  vpc_arn      = "${local.arn_prefix}:vpc/${aws_vpc.workers.id}"
}

data "aws_iam_policy_document" "launch" {
  # --- RunInstances, one statement per resource type it authorises ---------

  statement {
    sid       = "LaunchOnlyTaggedAllowListedWorkersWithTheWorkerProfile"
    actions   = ["ec2:RunInstances"]
    resources = [local.instance_arn]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/Project"
      values   = [local.project]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/Environment"
      values   = [var.environment]
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:InstanceType"
      values   = var.instance_types
    }
    condition {
      test     = "ArnEquals"
      variable = "ec2:InstanceProfile"
      values   = [local.worker_profile_arn]
    }
  }

  statement {
    sid       = "LaunchOnlyTaggedVolumes"
    actions   = ["ec2:RunInstances"]
    resources = [local.volume_arn]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/Project"
      values   = [local.project]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/Environment"
      values   = [var.environment]
    }
  }

  # Named subnets and the one security group. The network interface is
  # created in one of these subnets, so it needs no condition of its own.
  statement {
    sid     = "LaunchOnlyIntoTheWorkersNetwork"
    actions = ["ec2:RunInstances"]
    resources = concat(
      [for s in aws_subnet.workers : s.arn],
      [aws_security_group.workers.arn, "${local.arn_prefix}:network-interface/*"],
    )
  }

  # AMIs are read-only to a launcher. SkyPilot picks its GPU image per region
  # from its catalogue.
  statement {
    sid       = "LaunchFromAnyImage"
    actions   = ["ec2:RunInstances"]
    resources = ["arn:aws:ec2:${var.region}::image/*"]
  }

  # --- Tags ---------------------------------------------------------------

  statement {
    sid       = "TagWhatRunInstancesCreates"
    actions   = ["ec2:CreateTags"]
    resources = [local.instance_arn, local.volume_arn]

    condition {
      test     = "StringEquals"
      variable = "ec2:CreateAction"
      values   = ["RunInstances"]
    }
  }

  # SkyPilot re-tags the head node after launch (Name, ray-node-type) and
  # resumed nodes on start; it re-sends Project and Environment with them.
  statement {
    sid       = "RetagOnlyWorkersAndNeverOutOfThePool"
    actions   = ["ec2:CreateTags"]
    resources = [local.instance_arn, local.volume_arn]

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
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:RequestTag/Project"
      values   = [local.project]
    }
    condition {
      test     = "StringEqualsIfExists"
      variable = "aws:RequestTag/Environment"
      values   = [var.environment]
    }
  }

  # --- Lifecycle of existing workers --------------------------------------

  statement {
    sid       = "StopStartTerminateOnlyThisEnvironmentsWorkers"
    actions   = ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances"]
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

  # Describe* has no resource-level scoping. It reads, and SkyPilot needs it
  # to find its VPC, subnets, group, images and instances.
  statement {
    sid       = "Read"
    actions   = ["ec2:Describe*"]
    resources = ["*"]
  }

  statement {
    sid       = "AttachOnlyTheWorkerRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.worker.arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }

  statement {
    sid       = "ReadTheWorkerProfile"
    actions   = ["iam:GetInstanceProfile"]
    resources = [aws_iam_instance_profile.worker.arn]
  }

  # --- The boundary again, as Deny, so a later Allow cannot widen it -------

  statement {
    sid    = "DenyChangingAnyInstanceOutsideThisPool"
    effect = "Deny"
    actions = [
      "ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:RebootInstances",
      "ec2:ModifyInstanceAttribute", "ec2:ModifyInstanceMetadataOptions",
      "ec2:AssociateIamInstanceProfile", "ec2:ReplaceIamInstanceProfileAssociation",
    ]
    resources = [local.instance_arn]

    condition {
      test     = "StringNotEquals"
      variable = "ec2:ResourceTag/Project"
      values   = [local.project]
    }
  }

  statement {
    sid    = "DenyChangingAnotherEnvironmentsWorkers"
    effect = "Deny"
    actions = [
      "ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:RebootInstances",
      "ec2:ModifyInstanceAttribute", "ec2:ModifyInstanceMetadataOptions",
      "ec2:AssociateIamInstanceProfile", "ec2:ReplaceIamInstanceProfileAssociation",
    ]
    resources = [local.instance_arn]

    condition {
      test     = "StringNotEquals"
      variable = "ec2:ResourceTag/Environment"
      values   = [var.environment]
    }
  }

  # Tag-on-create has no ResourceTag yet, hence the CreateAction exemption;
  # RunInstances itself is held to the request tags above.
  statement {
    sid       = "DenyTaggingAnythingOutsideThisPool"
    effect    = "Deny"
    actions   = ["ec2:CreateTags"]
    resources = ["*"]

    condition {
      test     = "StringNotEquals"
      variable = "ec2:ResourceTag/Project"
      values   = [local.project]
    }
    condition {
      test     = "StringNotEquals"
      variable = "ec2:CreateAction"
      values   = ["RunInstances"]
    }
  }

  # Only when the request sets Project at all: a re-tag of Name alone is how
  # SkyPilot marks the head node. (StringNotEqualsIfExists would deny that,
  # since IfExists makes a missing key match.)
  statement {
    sid       = "DenyRetaggingOutOfThePool"
    effect    = "Deny"
    actions   = ["ec2:CreateTags", "ec2:RunInstances"]
    resources = ["*"]

    condition {
      test     = "Null"
      variable = "aws:RequestTag/Project"
      values   = ["false"]
    }
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestTag/Project"
      values   = [local.project]
    }
  }

  statement {
    sid       = "DenyRemovingTags"
    effect    = "Deny"
    actions   = ["ec2:DeleteTags"]
    resources = ["*"]
  }

  statement {
    sid       = "DenyInstanceTypesOffTheAllowList"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = [local.instance_arn]

    condition {
      test     = "StringNotEquals"
      variable = "ec2:InstanceType"
      values   = var.instance_types
    }
  }

  # On-demand only. The spot SKU is unavailable and the spot quota is 0, but
  # RunInstances with InstanceMarketOptions needs no permission beyond
  # RunInstances itself, so the quota alone would be the only thing refusing
  # a spot launch. Spot needs its own decision (ADR-0010).
  #
  # Plain StringEquals, not IfExists: the key is absent on the request's
  # other resources (subnet, image), and IfExists would deny those, i.e.
  # every launch.
  statement {
    sid       = "DenySpot"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = [local.instance_arn]

    condition {
      test     = "StringEquals"
      variable = "ec2:InstanceMarketType"
      values   = ["spot"]
    }
  }

  statement {
    sid       = "DenyOtherWaysToLaunch"
    effect    = "Deny"
    actions   = ["ec2:RequestSpotInstances", "ec2:RequestSpotFleet", "ec2:CreateFleet"]
    resources = ["*"]
  }

  statement {
    sid       = "DenyLaunchingIntoAnotherVpc"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.arn_prefix}:subnet/*", "${local.arn_prefix}:security-group/*"]

    condition {
      test     = "ArnNotEquals"
      variable = "ec2:Vpc"
      values   = [local.vpc_arn]
    }
  }
}

resource "aws_iam_role_policy" "launch" {
  name   = "launch-gpu-workers"
  role   = aws_iam_role.launch.id
  policy = data.aws_iam_policy_document.launch.json
}

# --- The worker's own identity -----------------------------------------------

data "aws_iam_policy_document" "worker_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "worker" {
  name               = local.worker_role_name
  description        = "Identity of unify-compute ${var.environment} GPU workers. Customer code can read it, so it is denied everything."
  assume_role_policy = data.aws_iam_policy_document.worker_trust.json
}

# No attached policy would already grant nothing. The explicit Deny is there so
# that attaching one later (say, to let workers pull from ECR) is visibly a
# decision to make this role do something, not a silent widening.
data "aws_iam_policy_document" "worker_deny_all" {
  statement {
    sid       = "CustomerCodeRunsHere"
    effect    = "Deny"
    actions   = ["*"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "worker_deny_all" {
  name   = "deny-all"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.worker_deny_all.json
}

# Named like the role: SkyPilot's remote_identity names the instance profile
# (sky/templates/aws-ray.yml.j2, IamInstanceProfile.Name).
resource "aws_iam_instance_profile" "worker" {
  name = local.worker_role_name
  role = aws_iam_role.worker.name
}
