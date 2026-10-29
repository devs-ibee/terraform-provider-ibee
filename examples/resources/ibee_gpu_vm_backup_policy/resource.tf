data "ibee_backup_storage_plans" "all" {}

resource "ibee_gpu_vm_backup_policy" "worker" {
  vm_id           = "VM_ID"
  frequency       = "daily"
  hour            = 2
  retention_days  = 7
  billing_catalog = data.ibee_backup_storage_plans.all.plans[0].billing_catalog
}
