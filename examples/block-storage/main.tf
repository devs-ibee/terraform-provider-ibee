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
variable "size_gb" {
  type        = number
  description = "Volume size in GB. Changes are sent to the resize operation; the API decides which sizes it accepts."
}

# The block storage plans the API offers at the site.
data "ibee_block_storage_plans" "available" {
  site_id = var.site_id
}

resource "ibee_block_volume" "data" {
  name     = "terraform-data"
  site_id  = var.site_id
  sku_code = data.ibee_block_storage_plans.available.plans[0].sku_code
  size_gb  = var.size_gb
}

# Import existing storage with:
# terraform import ibee_block_volume.data VOLUME_ID
# Attach the volume to a VM with ibee_cloud_vm_volume_attachment or
# ibee_gpu_vm_volume_attachment, passing ibee_block_volume.data.billing_catalog.

output "volume_id" {
  value = ibee_block_volume.data.id
}

output "volume_billing_catalog" {
  value = ibee_block_volume.data.billing_catalog
}
