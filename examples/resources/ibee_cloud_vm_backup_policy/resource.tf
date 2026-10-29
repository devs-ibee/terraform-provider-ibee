data "ibee_backup_storage_plans" "all" {}

resource "ibee_cloud_vm_backup_policy" "web" {
  vm_id           = "VM_ID"
  frequency       = "daily"
  hour            = 2
  retention_days  = 7
  billing_catalog = data.ibee_backup_storage_plans.all.plans[0].billing_catalog
}
