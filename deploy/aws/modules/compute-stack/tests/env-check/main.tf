# Test helper for user_data_env.tftest.hcl: compares the .env block of a
# rendered user-data with the variables docker-compose.yml passes to
# compute-api.

variable "user_data" {
  type = string
}

variable "compose_defaults_ok" {
  description = "compose variables user-data may leave to compose's default, with the reason."
  type        = map(string)
}

locals {
  env_lines = split("\n", regex("(?s)cat >\"\\$APP_DIR/\\.env\" <<ENV\\n(.*?)\\nENV\\n", var.user_data)[0])
  env_keys  = [for l in local.env_lines : split("=", l)[0] if can(regex("^[A-Z_][A-Z0-9_]*=", l))]

  compute_api_env = yamldecode(file("${path.module}/../../../../../compose/docker-compose.yml")).services["compute-api"].environment
  compose_vars    = toset(flatten([for v in values(local.compute_api_env) : flatten(regexall("\\$\\{([A-Z_][A-Z0-9_]*)", tostring(v)))]))
}

output "env_lines" {
  value = local.env_lines
}

output "compose_vars" {
  value = local.compose_vars
}

output "missing" {
  value = sort(setsubtract(local.compose_vars, toset(concat(local.env_keys, keys(var.compose_defaults_ok)))))
}
