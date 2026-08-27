terraform {
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

# token & workspace_id from IBEE_TOKEN / IBEE_WORKSPACE_ID
provider "ibee" {}

data "ibee_sites" "all" {}

resource "ibee_vpc" "example" {
  name    = "example-vpc"
  site_id = data.ibee_sites.all.sites[0].site_id
}

resource "ibee_firewall_group" "example" {
  name        = "example-fw"
  description = "managed by terraform"
}

output "vpc_id"            { value = ibee_vpc.example.id }
output "vpc_cidr"          { value = ibee_vpc.example.cidr }
output "default_subnet_id" { value = ibee_vpc.example.default_subnet_id }
