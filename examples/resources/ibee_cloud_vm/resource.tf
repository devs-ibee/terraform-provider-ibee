resource "ibee_cloud_vm" "web" {
  name             = "web"
  site_id          = "SITE_ID"
  plan_id          = "PLAN_ID"
  template_id      = "TEMPLATE_ID"
  os_distro        = "ubuntu"
  os_type          = "linux"
  billing_interval = "HOURLY" # or MONTHLY (a one-month commitment); HOURLY when omitted
  ssh_key_ids      = ["SSH_KEY_ID"]

  # Required by the API to destroy a VM with an automatically assigned public IP.
  delete_public_ip_action = "release"
}
