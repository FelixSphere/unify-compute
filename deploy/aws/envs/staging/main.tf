# Staging: everything production runs, plus its own console with an empty
# database (ADR-0004). Hostnames are children of compute.unifyai.us; none of
# them is a record that exists today.

module "stack" {
  source = "../../modules/compute-stack"

  region      = var.region
  environment = "staging"
  name        = "unify-compute-staging"
  # 10.70 is Unify Data's VPC in this account; ranges stay unique per stack so
  # any two can still be peered or put behind one VPN.
  vpc_cidr = "10.72.0.0/16"

  api_domain      = "staging.compute.unifyai.us"
  site_domain     = "site.staging.compute.unifyai.us"
  manage_site_dns = true

  # The customer app (ADR-0011). A new name, so always managed.
  app_domain = "app.staging.compute.unifyai.us"

  # The staging console runs on this instance and compute-api reaches it by
  # its compose service name. Never production's console (ADR-0004).
  console_domain = "console.staging.compute.unifyai.us"
  console_url    = "http://console:3000"
  bridge_enabled = true

  compute_api_image = var.compute_api_image
  site_image        = var.site_image
  console_image     = var.console_image
  ami_id            = var.ami_id

  # t4g.medium (4 GB) rather than the module's t4g.small (2 GB): the SkyPilot
  # API server is a Python process tree capped at 2 GB (docker-compose.skypilot.yml),
  # and SkyPilot itself refuses to size a resident worker pool below 2 GB
  # free. The rest of the stack used most of the small instance's 2 GB.
  # Changing it is a stop/start of the instance, not a replacement.
  instance_type = "t4g.medium"

  # The SkyPilot API server and its launch role (ADR-0010).
  skypilot = {
    config          = module.gpu_workers.skypilot_config
    launch_role_arn = module.gpu_workers.launch_role_arn
  }

  alb_deletion_protection = false
  alb_log_retention_days  = 30 # long enough to investigate last week, not an archive
  buckets_force_destroy   = true
  snapshot_backups        = false # staging data is disposable by design
}

# GPU workers for staging: their own VPC (10.73/16), the SkyPilot launch role,
# the worker identity, and the orphan reaper. See ADR-0010 for why they are in
# this account and what keeps them away from the other products in it.
module "gpu_workers" {
  source = "../../modules/gpu-workers"

  name        = "unify-compute-staging"
  environment = "staging"
  region      = var.region
  vpc_cidr    = "10.73.0.0/16"

  # Every zone that offers g5.xlarge in us-east-1 (all but use1-az3/us-east-1e
  # on 2026-10-03).
  availability_zones = ["us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1f"]

  # a10g-24gb-x1, the first SKU (compute-api/config/skus.json). Widening this
  # is a reviewed change, like a price.
  instance_types = ["g5.xlarge"]

  api_server_ip       = module.stack.public_ip
  api_server_role_arn = module.stack.instance_role_arn

  max_concurrent_workers = 1
  alert_email            = var.gpu_alert_email
}
