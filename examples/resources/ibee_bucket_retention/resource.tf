resource "ibee_bucket_retention" "assets" {
  bucket_name = "my-locked-bucket"
  mode        = "GOVERNANCE"
  days        = 30
}
