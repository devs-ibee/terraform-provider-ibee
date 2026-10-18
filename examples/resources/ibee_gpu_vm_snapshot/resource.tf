resource "ibee_gpu_vm_snapshot" "worker" {
  vm_id = "VM_ID"
  name  = "before-upgrade"
  mode  = "root_only"
  # Temporary: the API expects the snapshot storage SKU until it resolves it itself.
  billing_catalog = jsonencode({ sku_id = "SKU_ID", sku_code = "SKU_CODE" })
}
