resource "ibee_bucket" "records" {
  name                = "my-records"
  region              = "STORAGE_REGION"
  object_lock_enabled = true
}

# New objects can't be deleted or overwritten for 30 days.
resource "ibee_bucket_object_lock" "records" {
  bucket_name = ibee_bucket.records.name
  mode        = "GOVERNANCE"
  days        = 30
}
