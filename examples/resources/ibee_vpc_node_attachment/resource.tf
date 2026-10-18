resource "ibee_vpc_node_attachment" "web" {
  vpc_id    = "VPC_ID"
  subnet_id = "SUBNET_ID"
  vm_id     = "VM_ID"
}
