output "urls" {
  value = module.stack.urls
}

output "instance_id" {
  value = module.stack.instance_id
}

output "ami_id_in_use" {
  value = module.stack.ami_id_in_use
}

output "alb_dns_name" {
  value = module.stack.alb_dns_name
}

output "alb_zone_id" {
  value = module.stack.alb_zone_id
}

output "data_volume_id" {
  value = module.stack.data_volume_id
}
