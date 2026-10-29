resource "ibee_bucket" "assets" {
  name   = "my-assets"
  region = "REGION"
}

# Object lock is chosen at creation; the default retention can be changed later.
resource "ibee_bucket" "records" {
  name                = "my-records"
  region              = "REGION"
  object_lock_enabled = true
  default_retention = {
    mode = "GOVERNANCE"
    days = 30
  }
}
