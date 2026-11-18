data "ibee_load_balancer_regions" "available" {}

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
      name         = "app"
      protocol     = "HTTP"
      targets      = [{ kind = "ip", address = "10.0.0.10", port = 8080 }]
      health_check = { protocol = "HTTP", path = "/healthz" }
    },
    {
      name     = "api"
      protocol = "HTTP"
      targets  = [{ kind = "ip", address = "10.0.0.11", port = 8081 }]
    },
  ]
}
