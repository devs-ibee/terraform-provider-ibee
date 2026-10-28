data "ibee_snapshot_storage_plans" "all" {}

# Pass a plan's billing_catalog unchanged to ibee_cloud_vm_snapshot or ibee_gpu_vm_snapshot.
output "snapshot_billing_catalog" {
  value = data.ibee_snapshot_storage_plans.all.plans[0].billing_catalog
}
