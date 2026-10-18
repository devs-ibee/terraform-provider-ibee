resource "ibee_nat_port_forwarding_rule" "https" {
  vpc_id         = "VPC_ID"
  nat_gateway_id = "NAT_GATEWAY_ID"
  name           = "https"
  protocol       = "tcp"
  external_port  = 443
  internal_ip    = "10.0.0.10"
  internal_port  = 443
}
