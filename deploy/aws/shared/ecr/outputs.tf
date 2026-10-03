output "registry" {
  description = "The registry host the instances pull from and release.yml copies into."
  value       = split("/", aws_ecr_repository.this["unify-compute-api"].repository_url)[0]
}

output "repository_urls" {
  value = { for name, repo in aws_ecr_repository.this : name => repo.repository_url }
}
