action "ibee_vm_backup_run" "now" {
  config {
    vm_type         = "cloud"
    vm_id           = "VM_ID"
    reason          = "Before the database upgrade"
    billing_catalog = jsonencode({ sku_id = "SKU_ID", sku_code = "SKU_CODE" })
  }
}

# terraform apply -invoke=action.ibee_vm_backup_run.now
