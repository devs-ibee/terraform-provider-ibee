terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "registry_name" {
  type        = string
  description = "Globally unique registry name; it cannot be reused after deletion."
}

variable "sku_code" {
  type        = string
  description = "Storage plan SKU, for example REGISTRY-STORAGE-250GB."
}

# Waits until the registry is ACTIVE. Destroy needs an empty registry
# (`ibee registries empty REGISTRY_ID --confirm-name NAME`).
resource "ibee_container_registry" "apps" {
  name     = var.registry_name
  sku_code = var.sku_code
}

# The secret is returned once and kept in state (sensitive).
resource "ibee_container_registry_access_token" "push" {
  registry_id     = ibee_container_registry.apps.id
  name            = "ci-push"
  permission      = "PULL_PUSH"
  expires_in_days = 30
}

output "image_prefix" {
  value = "registry.ibee.ai/${ibee_container_registry.apps.name}"
}

output "docker_username" {
  value = ibee_container_registry_access_token.push.username
}

output "docker_password" {
  value     = ibee_container_registry_access_token.push.secret
  sensitive = true
}
