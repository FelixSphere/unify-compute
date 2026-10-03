# The runtime registry for unify-compute's own images (ADR-0009, "Runtime
# registry: ECR"). GHCR stays the build and provenance registry; release.yml
# copies each released digest from GHCR into these repositories, unchanged,
# before it cuts a host over, and the hosts pull from here with their instance
# role. No host ever holds a registry credential that outlives 12 hours.
#
# One pair of repositories for both environments: production only runs digests
# staging already ran, so the copy staging's release made is the one
# production pulls.
#
# Its own stack and state, not part of github-oidc: these repositories hold
# what every host boots from, and their lifetime should not be tied to a stack
# whose trust policies get reworked. The deploy roles (github-oidc) and the
# instance role (modules/compute-stack) build the repository ARNs from the same
# names rather than reading this state, so no stack depends on another's state.

locals {
  # Also named in deploy/aws/shared/github-oidc/main.tf,
  # deploy/aws/modules/compute-stack/instance.tf, scripts/release/host.sh and
  # .github/workflows/release.yml. Each matches the GHCR repository it is
  # copied from: ghcr.io/felixsphere/<name>.
  repositories = toset(["unify-compute-api", "unify-compute-site"])
}

resource "aws_ecr_repository" "this" {
  for_each = local.repositories

  name = each.key
  # Copies are tagged sha256-<digest> (release.yml), so a tag can only ever
  # name the one digest it was derived from. Immutability makes that a rule
  # rather than a convention.
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  # Every host boots and rolls back from these. Destroying one is an outage
  # that `terraform apply` would report as success.
  lifecycle {
    prevent_destroy = true
  }
}

# No repository policy: access is by IAM in this account only (the deploy
# roles push, the instance roles pull). Nothing is public.

resource "aws_ecr_lifecycle_policy" "this" {
  for_each = aws_ecr_repository.this

  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the last ${var.keep_images} released images"
      # Tagged copies only. A multi-arch index is stored as the tagged index
      # plus one untagged image per platform; a rule over "any" or
      # "untagged" would count, and could expire, the platform images a kept
      # index still points at. Those of an expired index are left behind:
      # a few MB each, a cost and not a break.
      selection = {
        tagStatus     = "tagged"
        tagPrefixList = ["sha256-"]
        countType     = "imageCountMoreThan"
        countNumber   = var.keep_images
      }
      action = { type = "expire" }
    }]
  })
}
