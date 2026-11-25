resource "ibee_cloud_vm_volume_attachment" "web" {
  vm_id     = "VM_ID"
  volume_id = "VOLUME_ID"
  # The volume's billing_catalog, for example ibee_block_volume.data.billing_catalog.
  billing_catalog = jsonencode({ sku_id = "SKU_ID", sku_code = "BLOCKSTO-NVME" })
}

# One data volume shared by several VMs as raw block storage. Only for cluster-aware
# software with its own locking and fencing: ext4, XFS or NTFS mounted on several VMs
# at once corrupts data.
resource "ibee_cloud_vm_volume_attachment" "shared" {
  for_each                = toset(["VM_ID_1", "VM_ID_2"])
  vm_id                   = each.key
  volume_id               = "SHARED_VOLUME_ID"
  mode                    = "multi-writer"
  shared_access_confirmed = true
  billing_catalog         = jsonencode({ sku_id = "SKU_ID", sku_code = "BLOCKSTO-NVME" })
}
