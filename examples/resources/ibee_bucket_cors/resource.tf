resource "ibee_bucket_cors" "assets" {
  bucket_name = "my-assets"
  rules = [{
    allowed_origins = ["https://app.example.com"]
    allowed_methods = ["GET", "HEAD"]
    max_age_seconds = 3600
  }]
}
