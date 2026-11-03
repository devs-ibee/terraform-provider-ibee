# Cancellation is available only while the restore is queued.
action "ibee_vm_backup_restore_cancel" "queued" {
  config {
    vm_type    = "cloud"
    restore_id = "RESTORE_ID"
  }
}

# terraform apply -invoke=action.ibee_vm_backup_restore_cancel.queued
