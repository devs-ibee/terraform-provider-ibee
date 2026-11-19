data "ibee_load_balancer_regions" "available" {}

# Pass one of these IDs as ibee_load_balancer.region_id.
output "load_balancer_regions" {
  value = data.ibee_load_balancer_regions.available.ids
}
