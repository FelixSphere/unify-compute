# The .env that user-data writes at first boot must give compute-api every
# variable docker-compose.yml passes it. A variable compose expects and
# user-data never writes falls back to compose's default on a rebuilt host:
# that is how a rebuild would have come back on console accounts with no
# Stripe after production moved to its own accounts (2026-10-03), refusing
# every uck_ key. The reverse gap, .env set and compose not passing it, was #67.
#
# Mocked provider: no credentials, no state, nothing created. Terraform >= 1.11
# (override_during), unlike the stacks.
#   make tf-test

mock_provider "aws" {
  # Known values at plan time too: the certificate's validation records are a
  # for_each over a computed attribute.
  override_during = plan

  mock_resource "aws_acm_certificate" {
    defaults = {
      arn                       = "arn:aws:acm:us-east-1:123456789012:certificate/mock"
      domain_validation_options = []
    }
  }
  # The provider validates ARNs even under a mock.
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/mock" }
  }
  mock_resource "aws_s3_bucket" {
    defaults = { arn = "arn:aws:s3:::mock" }
  }
  mock_resource "aws_lb" {
    defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/mock/0" }
  }
  mock_resource "aws_lb_target_group" {
    defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/mock/0" }
  }
  mock_resource "aws_lb_listener" {
    defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/mock/0/0" }
  }
  mock_data "aws_elb_service_account" {
    defaults = { arn = "arn:aws:iam::127311923021:root" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b"] }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{}" }
  }
}

# What user-data interpolates from other resources, fixed at plan time.
override_resource {
  target          = aws_ebs_volume.data
  override_during = plan
  values          = { id = "vol-00000000000000000" }
}
override_resource {
  target          = aws_s3_bucket.artifacts
  override_during = plan
  values          = { id = "mock-artifacts", arn = "arn:aws:s3:::mock-artifacts" }
}
override_resource {
  target          = aws_iam_role.artifacts_writer
  override_during = plan
  values          = { arn = "arn:aws:iam::123456789012:role/mock-artifacts-writer" }
}

variables {
  region            = "us-east-1"
  vpc_cidr          = "10.99.0.0/16"
  ami_id            = "ami-00000000000000000"
  compute_api_image = "123456789012.dkr.ecr.us-east-1.amazonaws.com/unify-compute-api@sha256:0000000000000000000000000000000000000000000000000000000000000000"
  site_image        = "123456789012.dkr.ecr.us-east-1.amazonaws.com/unify-compute-site@sha256:0000000000000000000000000000000000000000000000000000000000000000"

  alb_deletion_protection = false
  alb_log_retention_days  = 1
  buckets_force_destroy   = true
  snapshot_backups        = false
  manage_site_dns         = true

  # compose variables user-data deliberately leaves to compose's default.
  # Adding one is deciding that a rebuilt host runs on that default; say why.
  compose_defaults_ok = {
    UPSTREAMS                = "compose's default, aws, is what every host runs"
    ACCOUNTS_COMPUTE_DEFAULT = "unset on every host: an admin enables compute per account"
    MAIL_OUTBOX_DIR          = "development only; hosts send through SES"
  }
}

run "production" {
  command = plan

  variables {
    environment      = "production"
    name             = "unify-compute-production"
    api_domain       = "api.compute.unifyai.us"
    site_domain      = "compute.unifyai.us"
    app_domain       = "app.compute.unifyai.us"
    console_url      = "https://app.unifyapi.ai"
    bridge_enabled   = true
    accounts_backend = "local"
    stripe_enabled   = true
  }

  assert {
    condition = alltrue([
      strcontains(output.user_data, "STRIPE_SECRET_KEY=$(get_param '/unify-compute-production/stripe_secret_key' 2>/dev/null || true)"),
      strcontains(output.user_data, "STRIPE_WEBHOOK_SECRET=$(get_param '/unify-compute-production/stripe_webhook_secret' 2>/dev/null || true)"),
    ])
    error_message = "production must read both Stripe secrets from SSM, and a missing one must not stop the bootstrap."
  }
}

run "production_env" {
  command = plan

  module {
    source = "./tests/env-check"
  }

  variables {
    user_data = run.production.user_data
  }

  assert {
    condition     = length(output.missing) == 0
    error_message = "production's .env does not write ${join(", ", output.missing)}, which docker-compose.yml passes to compute-api. Render it in user-data.sh.tftpl, or add it to compose_defaults_ok with the reason a rebuilt host may run on compose's default."
  }

  assert {
    condition = alltrue([for line in [
      "ENVIRONMENT=production",
      "ACCOUNTS_BACKEND=local",
      "APP_ORIGIN=https://app.compute.unifyai.us",
      "SES_SENDER='Unify Compute <no-reply@compute.unifyai.us>'",
      "SES_CONFIGURATION_SET=unify-compute",
      "STRIPE_SECRET_KEY=$STRIPE_SECRET_KEY",
      "STRIPE_WEBHOOK_SECRET=$STRIPE_WEBHOOK_SECRET",
    ] : contains(output.env_lines, line)])
    error_message = "production's .env does not render the accounts and Stripe lines as the live host has them (deploy/releases/2026-10-03-production-accounts-live.md)."
  }

  # Guards the check itself: a regex that matched nothing would pass above.
  assert {
    condition     = contains(output.compose_vars, "ACCOUNTS_BACKEND") && contains(output.compose_vars, "POSTGRES_PASSWORD")
    error_message = "the compose variable extraction found nothing; fix tests/env-check before trusting this test."
  }
}

run "staging" {
  command = plan

  variables {
    environment      = "staging"
    name             = "unify-compute-staging"
    api_domain       = "staging.compute.unifyai.us"
    site_domain      = "site.staging.compute.unifyai.us"
    app_domain       = "app.staging.compute.unifyai.us"
    console_domain   = "console.staging.compute.unifyai.us"
    console_url      = "http://console:3000"
    console_image    = "ghcr.io/felixsphere/unifyapi-console@sha256:0000000000000000000000000000000000000000000000000000000000000000"
    bridge_enabled   = true
    accounts_backend = "local"
    stripe_enabled   = false
    skypilot = {
      config          = "{}"
      launch_role_arn = "arn:aws:iam::123456789012:role/unify-compute-staging-skypilot-launch"
    }
  }

  assert {
    condition = alltrue([
      !strcontains(output.user_data, "stripe_secret_key"),
      !strcontains(output.user_data, "stripe_webhook_secret"),
      strcontains(output.user_data, "\nSTRIPE_SECRET_KEY=\nSTRIPE_WEBHOOK_SECRET=\n"),
    ])
    error_message = "staging must not read Stripe secrets: compute-api refuses a live key outside production."
  }
}

run "staging_env" {
  command = plan

  module {
    source = "./tests/env-check"
  }

  variables {
    user_data = run.staging.user_data
  }

  assert {
    condition     = length(output.missing) == 0
    error_message = "staging's .env does not write ${join(", ", output.missing)}, which docker-compose.yml passes to compute-api. Render it in user-data.sh.tftpl, or add it to compose_defaults_ok with the reason a rebuilt host may run on compose's default."
  }

  assert {
    condition = alltrue([for line in [
      "ENVIRONMENT=staging",
      "ACCOUNTS_BACKEND=local",
      "APP_ORIGIN=https://app.staging.compute.unifyai.us",
      "SES_SENDER='Unify Compute <no-reply@compute.unifyai.us>'",
      "SES_CONFIGURATION_SET=unify-compute",
    ] : contains(output.env_lines, line)])
    error_message = "staging's .env does not render the accounts lines as the live host has them (deploy/releases/2026-10-03-staging-own-accounts.md)."
  }
}
