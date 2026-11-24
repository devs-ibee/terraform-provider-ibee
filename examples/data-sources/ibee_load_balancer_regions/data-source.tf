data "ibee_load_balancer_regions" "available" {}

# Pass one of these IDs as ibee_load_balancer.region_id.
output "load_balancer_regions" {
  value = data.ibee_load_balancer_regions.available.ids
}

# Regions whose load balancers can send traffic to private VPC addresses; elsewhere,
# targets are VMs (or addresses the region reaches publicly).
output "private_target_regions" {
  value = [for r in data.ibee_load_balancer_regions.available.regions : r.id if r.capabilities.private_targets]
}
