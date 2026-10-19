variable "vm_password" {
  type      = string
  sensitive = true
}

# Reset the Linux user's password and allow SSH password login.
action "ibee_vm_access" "reset_password" {
  config {
    vm_type               = "cloud"
    vm_id                 = "VM_ID"
    new_password          = var.vm_password
    password_auth_enabled = true
  }
}

# Add an inline public key.
action "ibee_vm_access" "add_key" {
  config {
    vm_type      = "cloud"
    vm_id        = "VM_ID"
    ssh_key_mode = "add"
    ssh_keys     = ["ssh-ed25519 AAAA... ops@example.com"]
  }
}

# terraform apply -invoke=action.ibee_vm_access.reset_password
