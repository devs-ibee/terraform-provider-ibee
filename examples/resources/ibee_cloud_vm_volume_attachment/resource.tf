resource "ibee_cloud_vm_volume_attachment" "web" {
  vm_id     = "VM_ID"
  volume_id = "VOLUME_ID"
  # The volume's billing_catalog, for example ibee_block_volume.data.billing_catalog.
  billing_catalog = jsonencode({ sku_id = "SKU_ID", sku_code = "BLOCKSTO-NVME" })
}
