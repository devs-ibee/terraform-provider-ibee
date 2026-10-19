# Create a new VM from a snapshot set. target_mode is replace, new_vm or volume_only.
action "ibee_vm_snapshot_restore" "to_new_vm" {
  config {
    vm_type         = "cloud"
    vm_id           = "VM_ID"
    snapshot_set_id = "SNAPSHOT_SET_ID"
    target_mode     = "new_vm"
    target_vm_name  = "web-restored"
    target_plan_id  = "PLAN_ID"
  }
}

# terraform apply -invoke=action.ibee_vm_snapshot_restore.to_new_vm
