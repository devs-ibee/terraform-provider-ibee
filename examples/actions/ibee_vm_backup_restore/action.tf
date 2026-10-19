# Restore a backup recovery point over the VM it was taken from.
action "ibee_vm_backup_restore" "replace" {
  config {
    vm_type           = "cloud"
    vm_id             = "VM_ID"
    recovery_point_id = "RECOVERY_POINT_ID"
    target_mode       = "replace"
  }
}

# terraform apply -invoke=action.ibee_vm_backup_restore.replace
