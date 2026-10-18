resource "ibee_firewall_rule" "https" {
  firewall_group_id = "FIREWALL_GROUP_ID"
  direction         = "ingress"
  protocol          = "tcp"
  port_start        = 443
  port_end          = 443
  remote_targets    = ["0.0.0.0/0"]
  action            = "allow"
}
