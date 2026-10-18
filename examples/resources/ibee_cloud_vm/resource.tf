resource "ibee_cloud_vm" "web" {
  name             = "web"
  site_id          = "SITE_ID"
  plan_id          = "PLAN_ID"
  template_id      = "TEMPLATE_ID"
  os_distro        = "ubuntu"
  billing_interval = "HOURLY"
  ssh_key_ids      = ["SSH_KEY_ID"]
}
