resource "ibee_bucket_lifecycle" "assets" {
  bucket_name = "my-assets"
  rules = [{
    status          = "Enabled"
    prefix          = "tmp/"
    expiration_days = 30
  }]
}
