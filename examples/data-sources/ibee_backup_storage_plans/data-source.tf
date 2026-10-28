data "ibee_backup_storage_plans" "all" {}

# Pass a plan's billing_catalog unchanged to ibee_cloud_vm_backup_policy or ibee_gpu_vm_backup_policy.
output "backup_billing_catalog" {
  value = data.ibee_backup_storage_plans.all.plans[0].billing_catalog
}
