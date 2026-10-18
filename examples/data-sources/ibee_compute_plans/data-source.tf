data "ibee_compute_plans" "cloud" {
  vm_type          = "cloud"
  site_id          = "SITE_ID"
  billing_interval = "HOURLY"
}
