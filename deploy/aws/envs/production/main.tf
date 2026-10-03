# Production: compute-api, the marketing site and Postgres. No console
# container and no Redis — the production console is app.unifyapi.ai, owned
# and released by the UnifyAPI CI/CD agent (ADR-0001, ADR-0002).
#
# compute-api stays dark until the console bridge is released there:
# bridge_enabled = false means COMPUTE_BRIDGE_SECRET is empty regardless of
# what exists in SSM.

module "stack" {
  source = "../../modules/compute-stack"

  region      = var.region
  environment = "production"
  name        = "unify-compute-production"
  vpc_cidr    = "10.71.0.0/16"

  api_domain  = "api.compute.unifyai.us"
  site_domain = "compute.unifyai.us"

  # False until the DNS cutover from OpenAI Sites, which is a separate,
  # deliberate step with its own rollback (ADR-0009). The certificate still
  # covers compute.unifyai.us, so the cutover does not wait on issuance.
  manage_site_dns = var.manage_site_dns

  # Always managed, so production's site can be smoke-tested on AWS before
  # compute.unifyai.us moves, and is still reachable on AWS after a rollback.
  site_aliases = ["site.compute.unifyai.us"]

  console_url    = "https://app.unifyapi.ai"
  bridge_enabled = false

  compute_api_image = var.compute_api_image
  site_image        = var.site_image
  ami_id            = var.ami_id

  # Off: production gets the SkyPilot API server only after staging has run
  # real jobs through it (ADR-0010). Turning it on also needs instance_type
  # raised as on staging, the SSM admin password, and the one-time host setup.
  skypilot = var.gpu_workers_enabled ? {
    config          = module.gpu_workers[0].skypilot_config
    launch_role_arn = module.gpu_workers[0].launch_role_arn
  } : null

  alb_deletion_protection = true
  alb_log_retention_days  = 90
  buckets_force_destroy   = false
  snapshot_backups        = true
}

# Production GPU workers: defined, not created. 10.74/16 is reserved for them
# so the range stays unique in the account (ADR-0010).
module "gpu_workers" {
  source = "../../modules/gpu-workers"
  count  = var.gpu_workers_enabled ? 1 : 0

  name               = "unify-compute-production"
  environment        = "production"
  region             = var.region
  vpc_cidr           = "10.74.0.0/16"
  availability_zones = ["us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1f"]
  instance_types     = ["g5.xlarge"]

  api_server_ip       = module.stack.public_ip
  api_server_role_arn = module.stack.instance_role_arn

  max_concurrent_workers = 1
  alert_email            = var.gpu_alert_email
}
