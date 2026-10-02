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

  console_url    = "https://app.unifyapi.ai"
  bridge_enabled = false

  compute_api_image = var.compute_api_image
  site_image        = var.site_image
  ami_id            = var.ami_id

  alb_deletion_protection = true
  alb_log_retention_days  = 90
  alb_logs_force_destroy  = false
  snapshot_backups        = true
}
