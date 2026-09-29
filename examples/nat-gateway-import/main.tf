terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "vpc_id" {
  description = "VPC ID that owns an existing, separately managed NAT gateway."
  type        = string
}

variable "nat_gateway_id" {
  description = "Existing NAT gateway ID to import into this Terraform state."
  type        = string
}

resource "ibee_nat_gateway" "existing" {
  vpc_id = var.vpc_id
}

output "nat_gateway_id" {
  value = ibee_nat_gateway.existing.id
}

# Import before planning or applying this root:
# terraform import ibee_nat_gateway.existing "$TF_VAR_vpc_id/$TF_VAR_nat_gateway_id"
#
# Do not use this resource for the default NAT gateway created and owned by
# ibee_vpc. Reference ibee_vpc.default_nat_gateway_id in that case. Creation
# refuses to adopt an existing gateway; this example is for imported gateways.
