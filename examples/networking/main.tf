terraform {
  required_version = ">= 1.11"
  required_providers {
    ibee = {
      source  = "devs-ibee/ibee"
      version = "~> 0.1"
    }
  }
}

# Set IBEE_TOKEN and IBEE_WORKSPACE_ID in the environment.
provider "ibee" {}

variable "site_id" {
  description = "Optional available site ID from ibee_network_sites; defaults to the first available networking site."
  type        = string
  default     = null
}
variable "vm_id" {
  description = "Existing cloud/GPU VM ID in the same workspace and site."
  type        = string
}
variable "backend_ip" {
  description = "IP address reachable by the load balancer."
  type        = string
}

data "ibee_network_sites" "available" {
  lifecycle {
    postcondition {
      condition = var.site_id == null ? length([
        for site in self.sites : site.site_id if site.available
        ]) > 0 : contains([
        for site in self.sites : site.site_id if site.available
      ], var.site_id)
      error_message = "The networking service must report an available site. Choose an available ibee_network_sites entry; compute availability alone is insufficient."
    }
  }
}

locals {
  network_site_id = var.site_id != null ? var.site_id : try(sort([
    for site in data.ibee_network_sites.available.sites : site.site_id if site.available
  ])[0], null)
}

# Keep subnet ownership explicit. Imported VPCs do not adopt child subnets.
resource "ibee_vpc" "app" {
  name                  = "terraform-network"
  site_id               = local.network_site_id
  cidr                  = "10.144.0.0/22"
  create_default_subnet = false
}
resource "ibee_vpc_subnet" "app" {
  vpc_id = ibee_vpc.app.id
  name   = "app"
  cidr   = "10.144.0.0/24"
}
resource "ibee_nat_gateway" "app" {
  vpc_id    = ibee_vpc.app.id
  subnet_id = ibee_vpc_subnet.app.id
  name      = "app-egress"
}
resource "ibee_vpc_node_attachment" "app" {
  vpc_id       = ibee_vpc.app.id
  subnet_id    = ibee_vpc_subnet.app.id
  vm_id        = var.vm_id
  connectivity = "nat"
  depends_on   = [ibee_nat_gateway.app]
}
resource "ibee_nat_port_forwarding_rule" "https" {
  vpc_id         = ibee_vpc.app.id
  nat_gateway_id = ibee_nat_gateway.app.id
  name           = "https"
  protocol       = "tcp"
  external_port  = 443
  internal_ip    = ibee_vpc_node_attachment.app.private_ip
  internal_port  = 443
}

resource "ibee_firewall_group" "app" {
  name        = "app-firewall"
  description = "Managed by Terraform"
}
resource "ibee_firewall_rule" "https" {
  firewall_group_id = ibee_firewall_group.app.id
  direction         = "ingress"
  protocol          = "tcp"
  port_start        = 443
  port_end          = 443
  remote_targets    = ["0.0.0.0/0"]
  action            = "allow"
}
resource "ibee_firewall_attachment" "app" {
  firewall_group_id = ibee_firewall_group.app.id
  vm_id             = var.vm_id
  depends_on        = [ibee_firewall_rule.https]
}

# Reserve a separate address for a VM that needs direct public connectivity.
# Do not attach this IP to the same VM as the NAT-only workflow above.
variable "public_vm_id" {
  description = "A second VM to receive the reserved public IP."
  type        = string
}
resource "ibee_reserved_ip" "public" {
  site_id = local.network_site_id
  label   = "terraform-public-ip"
}
resource "ibee_reserved_ip_attachment" "public" {
  reserved_ip_id = ibee_reserved_ip.public.id
  vm_id          = var.public_vm_id
}

resource "ibee_load_balancer_l4" "tcp" {
  name     = "app-tcp"
  protocol = "tcp"
  backends = [{
    type   = "ip"
    target = var.backend_ip
    port   = 443
  }]
}
resource "ibee_load_balancer_l7" "http" {
  name     = "app-http"
  protocol = "http"
  backends = [{
    type   = "ip"
    target = var.backend_ip
    port   = 8080
  }]
  rules = [{
    path_prefix = "/api"
    backends = [{
      type   = "ip"
      target = var.backend_ip
      port   = 8081
    }]
  }]
}

output "private_ip" {
  value = ibee_vpc_node_attachment.app.private_ip
}
output "reserved_ip" {
  value = ibee_reserved_ip.public.address
}
output "http_endpoint" {
  value = ibee_load_balancer_l7.http.endpoint_url
}
