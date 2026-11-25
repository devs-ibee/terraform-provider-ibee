data "ibee_load_balancer_regions" "available" {}

variable "web_vm_ids" {
  description = "VMs with their own public IP that serve the site."
  type        = list(string)
}

# Each node is billed per hour; node_count sets how many run.
resource "ibee_load_balancer" "web" {
  name         = "web"
  region_id    = data.ibee_load_balancer_regions.available.ids[0]
  traffic_type = "http"

  listeners = [{
    name         = "http"
    protocol     = "HTTP"
    port         = 80
    target_group = "app"
    rules        = [{ path_prefix = "/api", target_group = "api" }]
  }]

  target_groups = [
    {
      name     = "app"
      protocol = "HTTP"
      targets  = [for id in var.web_vm_ids : { kind = "vm", resource_id = id, port = 8080 }]
      # unhealthy_threshold is left to the server (1 for HTTP, 3 for TCP).
      health_check = { protocol = "HTTP", path = "/healthz", hostname = "app.example.com", expected_statuses = [200, 204] }
    },
    {
      name     = "api"
      protocol = "HTTP"
      targets  = [for id in var.web_vm_ids : { kind = "vm", resource_id = id, port = 8081 }]
    },
  ]

  # Only these sources reach the listener; leave firewall out to accept everyone.
  firewall = {
    mode  = "restricted"
    rules = [{ address_family = "IPv4", protocol = "TCP", source = "203.0.113.0/24", port = 80 }]
  }
}

# Point an A record for your domain at this address.
output "load_balancer_address" {
  value = ibee_load_balancer.web.address
}
