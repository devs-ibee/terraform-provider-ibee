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

variable "secret_namespace" {
  description = "Vault namespace for the workspace's Secret Manager, 3 to 32 lowercase letters, digits or hyphens. It can't change later."
  type        = string
  default     = "terraform-example"
}

resource "ibee_bucket" "assets" {
  name      = var.bucket_name
  region    = var.storage_region
  is_public = false
}

# Secret Manager: Terraform manages the service, secret settings and app
# logins. Secret values never pass through Terraform; write them with the
# Vault API, for example `bao kv put`.
resource "ibee_secret_manager" "this" {
  namespace = var.secret_namespace
}

resource "ibee_secret_manager_secret" "database" {
  path            = "apps/billing/db"
  custom_metadata = { owner = "billing" }
  max_versions    = 10
  depends_on      = [ibee_secret_manager.this]
}

resource "ibee_secret_manager_app_credential" "billing" {
  name       = "billing-app"
  access     = "read"
  folder     = "apps/billing"
  depends_on = [ibee_secret_manager.this]
}

output "bucket_name" {
  value = ibee_bucket.assets.name
}

output "vault_endpoint" {
  value = ibee_secret_manager.this.endpoint
}

output "app_role_id" {
  value = ibee_secret_manager_app_credential.billing.role_id
}

output "app_secret_id" {
  value     = ibee_secret_manager_app_credential.billing.secret_id
  sensitive = true
}

# Destroy deletes the bucket (the API refuses one that still holds files),
# permanently deletes the secret and every version of its value, and revokes
# the app login. Secret Manager itself stays on.
