resource "ibee_cloud_vm_backup_policy" "web" {
  vm_id          = "VM_ID"
  frequency      = "daily"
  hour           = 2
  retention_days = 7
  # Temporary: the API expects the backup storage SKU until it resolves it itself.
  billing_catalog = jsonencode({ sku_id = "SKU_ID", sku_code = "SKU_CODE" })
}
