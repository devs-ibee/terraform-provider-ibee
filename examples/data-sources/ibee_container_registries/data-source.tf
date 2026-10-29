data "ibee_container_registries" "all" {}

output "registry_names" {
  value = [for r in data.ibee_container_registries.all.registries : r.name]
}
