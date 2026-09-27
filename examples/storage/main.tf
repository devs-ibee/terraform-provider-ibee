terraform {
  required_version = ">= 1.11.0"

  required_providers {
    ibee = {
      source = "devs-ibee/ibee"
    }
  }
}

# Authentication uses IBEE_TOKEN and IBEE_WORKSPACE_ID. For the development
# environment set IBEE_ENDPOINT=https://api.ibee.co.in/v1.
provider "ibee" {}

variable "bucket_name" {
  description = "An available bucket name in the selected workspace."
  type        = string
}

variable "storage_region" {
  description = "An Object Storage region accepted by this environment; not a compute site ID."
  type        = string
}

variable "secret_store_name" {
  description = "An available secret store name. Archived stores retain their names."
  type        = string
  default     = "terraform-example"
}

variable "database_password" {
  description = "Provide through TF_VAR_database_password or an ephemeral source. It is not persisted in plan or state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "secret_rotation_version" {
  description = "Increase when changing database_password. Changing a write-only value alone cannot trigger an update."
  type        = number
  default     = 0

  validation {
    condition     = var.secret_rotation_version >= 0 && floor(var.secret_rotation_version) == var.secret_rotation_version
    error_message = "Use a nonnegative integer and increase it for every rotation."
  }
}

resource "ibee_bucket" "assets" {
  name                = var.bucket_name
  region              = var.storage_region
  is_public           = false
  object_lock_enabled = false
  force_destroy       = false
}

resource "ibee_secret_store" "application" {
  name          = var.secret_store_name
  description   = "Application secrets managed by Terraform"
  force_archive = false
}

resource "ibee_secret" "database" {
  store_id         = ibee_secret_store.application.id
  secret_name      = "database-password"
  value_wo         = jsonencode({ password = var.database_password })
  value_wo_version = var.secret_rotation_version
}

output "bucket_name" {
  value = ibee_bucket.assets.name
}

output "secret_id" {
  value = ibee_secret.database.id
}

# Destroy soft-deletes the latest secret value, then archives the store; history
# and reserved names remain. Buckets refuse destroy while reported nonempty.
# Stop object writers before destroy: the API has no atomic empty-bucket delete.
# To deliberately delete all objects, apply force_destroy=true before destroy.
