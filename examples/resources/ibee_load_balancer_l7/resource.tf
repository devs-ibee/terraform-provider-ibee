resource "ibee_load_balancer_l7" "http" {
  name     = "http"
  protocol = "http"
  backends = [{
    type   = "ip"
    target = "10.0.0.10"
    port   = 8080
  }]
}
