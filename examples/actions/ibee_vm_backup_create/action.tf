# The VM must have an enabled backup policy. Its stored catalog is reused.
action "ibee_vm_backup_create" "before_upgrade" {
  config {
    vm_type = "cloud"
    vm_id   = "VM_ID"
  }
}

# terraform apply -invoke=action.ibee_vm_backup_create.before_upgrade
