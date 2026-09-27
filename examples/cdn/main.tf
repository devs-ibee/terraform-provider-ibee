terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}
provider "ibee" {}

variable "bucket_name_or_id" {
  type        = string
  description = "Existing public bucket name or canonical ID; Terraform does not upload objects."
}

resource "ibee_cdn_distribution" "assets" {
  name         = "terraform-assets"
  origin_type  = "bucket"
  origin_id    = var.bucket_name_or_id
  cache_policy = "static-assets"
  enabled      = true
}

# Enable only after index.html exists in the origin bucket.
variable "enable_spa" {
  type    = bool
  default = false
}
resource "ibee_cdn_website" "app" {
  count           = var.enable_spa ? 1 : 0
  distribution_id = ibee_cdn_distribution.assets.id
  index_document  = "index.html"
}

variable "domain" {
  type        = string
  default     = null
  description = "Optional domain you own. Configure its CNAME using the output; TLS may remain pending until DNS verification."
}
resource "ibee_cdn_domain" "assets" {
  count           = var.domain == null ? 0 : 1
  distribution_id = ibee_cdn_distribution.assets.id
  domain          = var.domain
}
output "cdn_url" {
  value = ibee_cdn_distribution.assets.default_url
}
output "domain_cname" {
  value = var.domain == null ? null : {
    name   = ibee_cdn_domain.assets[0].cname_name
    target = ibee_cdn_domain.assets[0].cname_target
  }
}

# Custom origins use ibee_cdn_origin with name and origin_url (HTTPS).
# The inspected development gateway returned 404 for /cdn/origins; confirm that
# route is deployed before selecting origin_type="custom".
