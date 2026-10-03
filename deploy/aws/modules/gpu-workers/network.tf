# A VPC for GPU workers and nothing else. Customer code runs here, so the
# design question is what it can reach, not what can reach it:
#
# - No peering, no transit gateway, no VPC endpoint shared with another VPC.
#   There is no route from this VPC to the compute host's VPC or to any other
#   product's. The SkyPilot API server reaches workers over their public IP,
#   and the security group admits only the compute host's Elastic IP.
#   Peering would have put Postgres one route and one security-group edit
#   away from a customer's container.
# - Public subnets and an internet gateway, no NAT gateway. Workers need
#   egress (image pulls, Hugging Face, S3) and a public IP costs USD 0.005/h
#   against a NAT gateway's ~USD 33/month plus per-GB processing.
#
# The Name tags are what SkyPilot looks up (aws.vpc_names and
# aws.security_group_name in the config this module outputs), so they are
# load-bearing, not decoration.

resource "aws_vpc" "workers" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.workers_name }
}

resource "aws_internet_gateway" "workers" {
  vpc_id = aws_vpc.workers.id
  tags   = { Name = local.workers_name }
}

resource "aws_subnet" "workers" {
  for_each = { for i, az in var.availability_zones : az => i }

  vpc_id                  = aws_vpc.workers.id
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, each.value)
  availability_zone       = each.key
  map_public_ip_on_launch = true # SkyPilot (use_internal_ips: false) only picks subnets that do
  tags                    = { Name = "${local.workers_name}-${each.key}" }
}

resource "aws_route_table" "workers" {
  vpc_id = aws_vpc.workers.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.workers.id
  }

  tags = { Name = local.workers_name }
}

resource "aws_route_table_association" "workers" {
  for_each = aws_subnet.workers

  subnet_id      = each.value.id
  route_table_id = aws_route_table.workers.id
}

# Pre-created so SkyPilot never creates or edits one. Its own default opens
# SSH to 0.0.0.0/0 (sky/provision/aws/config.py, _configure_security_group);
# given a group that already has rules, it leaves the rules alone, and the
# launch role has no ec2:CreateSecurityGroup or Authorize* to do otherwise.
resource "aws_security_group" "workers" {
  name        = local.workers_name
  description = "GPU workers: SSH from the ${var.environment} compute host only"
  vpc_id      = aws_vpc.workers.id

  ingress {
    description = "SSH from the SkyPilot API server (compute host Elastic IP)"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["${var.api_server_ip}/32"]
  }

  # Nodes of one multi-node cluster talk to each other. Single-node today;
  # harmless until then, and SkyPilot's own group has the same rule.
  ingress {
    description = "Between workers in this group"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    self        = true
  }

  egress {
    description = "Image pulls, model downloads, customer buckets"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = local.workers_name }
}
