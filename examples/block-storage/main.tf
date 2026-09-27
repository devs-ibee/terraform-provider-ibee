terraform {
  required_providers {
    ibee = {
      source = "devs-ibee/ibee"
    }
  }
}

# Set IBEE_TOKEN and IBEE_WORKSPACE_ID in your environment.
provider "ibee" {}

variable "site_id" {
  type        = string
  description = "Site identifier from the trusted IBEE catalog."
}
variable "sku_code" {
  type        = string
  description = "Active block-storage SKU confirmed in the portal. Public catalog discovery is deployment-dependent; never invent a SKU or price."
}
variable "size_gb" {
  type        = number
  description = "An allowed size for this SKU. Increasing expands in place; decreasing replaces the volume and loses its data."
}

resource "ibee_block_volume" "data" {
  name         = "terraform-data"
  site_id      = var.site_id
  sku_code     = var.sku_code
  size_gb      = var.size_gb
  volume_class = "balanced"
  vm_type      = "cloud"

  # Attached expansion requires the backend's online-resize checks to pass.
  allow_online_resize = false
}

# Import existing storage with:
# terraform import ibee_block_volume.data VOLUME_ID
# Purchases currently require an INR organization because the public facade
# selects an INR catalog. The service computes authoritative SKU pricing.
# Detach every attachment before destroying a volume. Deletion is never forced.

output "volume_id" {
  value = ibee_block_volume.data.id
}

# Optional storage-node attachment (does not hot-plug a VM guest):
# resource "ibee_block_volume_attachment" "node" {
#   volume_id = ibee_block_volume.data.id
#   node_name = var.canonical_storage_node
#   mode      = "single-writer"
#   # Set true and apply only after unmounting, then destroy the attachment.
#   confirm_unmounted = false
# }
# Import using VOLUME_ID/NODE_NAME. Use a cloud/GPU VM volume attachment
# resource instead when you need VM guest integration; do not manage both.
