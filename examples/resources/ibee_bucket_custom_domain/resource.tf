# The bucket must be public: the API refuses a custom domain on a private one.
resource "ibee_bucket_custom_domain" "assets" {
  bucket_name = "my-assets"
  domain      = "assets.example.com"
}

# Create a CNAME record for assets.example.com that points here.
output "cname_target" {
  value = ibee_bucket_custom_domain.assets.cname_target
}
