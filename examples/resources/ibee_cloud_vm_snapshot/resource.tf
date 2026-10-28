data "ibee_snapshot_storage_plans" "all" {}

resource "ibee_cloud_vm_snapshot" "web" {
  vm_id           = "VM_ID"
  name            = "before-upgrade"
  mode            = "root_only"
  billing_catalog = data.ibee_snapshot_storage_plans.all.plans[0].billing_catalog
}
