# Staging: everything production runs, plus its own console with an empty
# database (ADR-0004). Hostnames are children of compute.unifyai.us; none of
# them is a record that exists today.

module "stack" {
  source = "../../modules/compute-stack"

  region      = var.region
  environment = "staging"
  name        = "unify-compute-staging"
  vpc_cidr    = "10.70.0.0/16"

  api_domain      = "staging.compute.unifyai.us"
  site_domain     = "site.staging.compute.unifyai.us"
  manage_site_dns = true

  # The staging console runs on this instance and compute-api reaches it by
  # its compose service name. Never production's console (ADR-0004).
  console_domain = "console.staging.compute.unifyai.us"
  console_url    = "http://console:3000"
  bridge_enabled = true

  compute_api_image = var.compute_api_image
  site_image        = var.site_image
  console_image     = var.console_image
  ami_id            = var.ami_id

  alb_deletion_protection = false
  alb_log_retention_days  = 30 # long enough to investigate last week, not an archive
  buckets_force_destroy   = true
  snapshot_backups        = false # staging data is disposable by design
}
