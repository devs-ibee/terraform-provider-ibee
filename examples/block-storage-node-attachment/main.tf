terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "volume_id" {
  description = "Existing standalone block volume ID. Do not also attach it through a VM volume attachment resource."
  type        = string
}

variable "canonical_storage_node" {
  description = "Canonical storage node name accepted by the storage service."
  type        = string
}

resource "ibee_block_volume_attachment" "storage_node" {
  volume_id         = var.volume_id
  node_name         = var.canonical_storage_node
  mode              = "single-writer"
  confirm_unmounted = false
}

output "device_path" {
  value       = ibee_block_volume_attachment.storage_node.device_path
  description = "Device path reported by the storage node; this resource does not mount it inside a VM."
}

# Before terraform destroy, unmount and stop using the device, set
# confirm_unmounted = true, apply that change, then destroy the attachment.
