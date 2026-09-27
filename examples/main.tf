terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

# Credentials and workspace come from IBEE_TOKEN / IBEE_WORKSPACE_ID.
# Set IBEE_ENV=dev for the development API.
provider "ibee" {}

variable "site_id" {
  type        = string
  description = "An available site from the ibee_sites catalog."
}

data "ibee_sites" "all" {}
data "ibee_billing_eligibility" "account" {}

resource "ibee_vpc" "example" {
  name    = "example-vpc"
  site_id = var.site_id
}

resource "ibee_firewall_group" "example" {
  name        = "example-fw"
  description = "managed by terraform"
}

output "vpc_id" { value = ibee_vpc.example.id }
output "default_subnet_id" { value = ibee_vpc.example.default_subnet_id }
output "billing_allowed" { value = data.ibee_billing_eligibility.account.allowed }
output "billing_reason" { value = data.ibee_billing_eligibility.account.reason }
