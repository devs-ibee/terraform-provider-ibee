resource "ibee_bucket_notifications" "assets" {
  bucket_name = "my-assets"
  configs = [{
    events      = ["upload", "delete"]
    webhook_url = "https://hooks.example.com/ibee"
  }]
}
