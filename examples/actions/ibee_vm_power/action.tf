action "ibee_vm_power" "start" {
  config {
    vm_id     = "VM_ID"
    vm_type   = "cloud"
    operation = "start"
  }
}

# terraform apply -invoke=action.ibee_vm_power.start
