terraform {
  required_providers {
    ibee = {
      source = "devs-ibee/ibee"
    }
  }
}

# Set IBEE_TOKEN and IBEE_WORKSPACE_ID in your environment.
provider "ibee" {
  operation_timeout = "30m"
}

variable "site_id" { type = string }
variable "cloud_plan_id" { type = string }
variable "cloud_template_id" { type = string }
variable "cloud_os_distro" { type = string }
variable "gpu_plan_id" { type = string }
variable "gpu_template_id" { type = string }
variable "gpu_os_distro" { type = string }
variable "ssh_key_ids" { type = set(string) }
variable "cloud_volume_id" { type = string }
variable "gpu_volume_id" { type = string }
variable "cloud_volume_billing_catalog" {
  type        = string
  description = "billing_catalog of the cloud volume, for example ibee_block_volume.<name>.billing_catalog."
}
variable "gpu_volume_billing_catalog" {
  type        = string
  description = "billing_catalog of the GPU volume, for example ibee_block_volume.<name>.billing_catalog."
}

variable "confirm_volumes_unmounted" {
  type        = bool
  default     = false
  description = "Sent as confirm_unmounted with detach-volume when the attachments are destroyed."
}

data "ibee_compute_plans" "cloud" {
  vm_type = "cloud"
  site_id = var.site_id
}
data "ibee_compute_plans" "gpu" {
  vm_type = "gpu"
  site_id = var.site_id
}
data "ibee_images" "cloud" { vm_type = "cloud" }
data "ibee_images" "gpu" { vm_type = "gpu" }

# The SKUs snapshots and backups are billed under.
data "ibee_snapshot_storage_plans" "all" {}
data "ibee_backup_storage_plans" "all" {}

resource "ibee_cloud_vm" "app" {
  name             = "terraform-app"
  site_id          = var.site_id
  plan_id          = var.cloud_plan_id
  template_id      = var.cloud_template_id
  os_distro        = var.cloud_os_distro
  os_type          = "linux"
  billing_interval = "HOURLY"
  ssh_key_ids      = var.ssh_key_ids
  tags             = ["terraform", "app"]
}
resource "ibee_gpu_vm" "worker" {
  name             = "terraform-gpu-worker"
  site_id          = var.site_id
  plan_id          = var.gpu_plan_id
  template_id      = var.gpu_template_id
  os_distro        = var.gpu_os_distro
  os_type          = "linux"
  billing_interval = "HOURLY"
  ssh_key_ids      = var.ssh_key_ids
  tags             = ["terraform", "worker"]
}

# Existing volumes in the same workspace and site. The attach-volume operation
# needs the volume's billing_catalog. The API returns mount guidance; the
# provider does not run commands in the guests.
resource "ibee_cloud_vm_volume_attachment" "app_data" {
  vm_id             = ibee_cloud_vm.app.id
  volume_id         = var.cloud_volume_id
  billing_catalog   = var.cloud_volume_billing_catalog
  confirm_unmounted = var.confirm_volumes_unmounted
}
resource "ibee_gpu_vm_volume_attachment" "worker_data" {
  vm_id             = ibee_gpu_vm.worker.id
  volume_id         = var.gpu_volume_id
  billing_catalog   = var.gpu_volume_billing_catalog
  confirm_unmounted = var.confirm_volumes_unmounted
}

resource "ibee_cloud_vm_snapshot" "app" {
  vm_id           = ibee_cloud_vm.app.id
  name            = "terraform-initial-app"
  mode            = "all_attached"
  billing_catalog = data.ibee_snapshot_storage_plans.all.plans[0].billing_catalog
  depends_on      = [ibee_cloud_vm_volume_attachment.app_data]
}
resource "ibee_gpu_vm_snapshot" "worker" {
  vm_id           = ibee_gpu_vm.worker.id
  name            = "terraform-initial-worker"
  mode            = "all_attached"
  billing_catalog = data.ibee_snapshot_storage_plans.all.plans[0].billing_catalog
  depends_on      = [ibee_gpu_vm_volume_attachment.worker_data]
}

# Destroy disables backups. Backups already taken are kept and may still be billed.
# Settings that are not configured are not sent; the API's defaults apply.
resource "ibee_cloud_vm_backup_policy" "app" {
  vm_id           = ibee_cloud_vm.app.id
  frequency       = "daily"
  timezone        = "UTC"
  hour            = 12
  retention_days  = 7
  billing_catalog = data.ibee_backup_storage_plans.all.plans[0].billing_catalog
}
resource "ibee_gpu_vm_backup_policy" "worker" {
  vm_id           = ibee_gpu_vm.worker.id
  frequency       = "weekly"
  timezone        = "UTC"
  day_of_week     = 6
  retention_days  = 14
  billing_catalog = data.ibee_backup_storage_plans.all.plans[0].billing_catalog
}

output "app_public_ip" { value = ibee_cloud_vm.app.public_ip }
output "worker_public_ip" { value = ibee_gpu_vm.worker.public_ip }
output "app_mount_instructions" { value = ibee_cloud_vm_volume_attachment.app_data.mount_instructions }
output "worker_mount_instructions" { value = ibee_gpu_vm_volume_attachment.worker_data.mount_instructions }
