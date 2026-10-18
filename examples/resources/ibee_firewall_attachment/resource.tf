resource "ibee_firewall_attachment" "web" {
  firewall_group_id = "FIREWALL_GROUP_ID"
  vm_id             = "VM_ID"
}
