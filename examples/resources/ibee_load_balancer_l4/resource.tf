resource "ibee_load_balancer_l4" "tcp" {
  name     = "tcp"
  protocol = "tcp"
  backends = [{
    type   = "ip"
    target = "10.0.0.10"
    port   = 443
  }]
}
