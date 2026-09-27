terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "enable_custom_origin" {
  description = "Enable only after confirming the custom-origin API route is deployed in the selected environment."
  type        = bool
  default     = false
}

variable "origin_url" {
  description = "HTTPS URL of an origin server that you operate."
  type        = string
}

resource "ibee_cdn_origin" "application" {
  count      = var.enable_custom_origin ? 1 : 0
  name       = "terraform-custom-origin"
  origin_url = var.origin_url
}

resource "ibee_cdn_distribution" "application" {
  count        = var.enable_custom_origin ? 1 : 0
  name         = "terraform-custom-origin-cdn"
  origin_type  = "custom"
  origin_id    = ibee_cdn_origin.application[0].id
  cache_policy = "static-assets"
  enabled      = true
}

output "cdn_url" {
  value = var.enable_custom_origin ? ibee_cdn_distribution.application[0].default_url : null
}

# Destroy the distribution before deleting its custom origin. This manages the
# CDN origin record; it does not create or manage the origin server itself.
