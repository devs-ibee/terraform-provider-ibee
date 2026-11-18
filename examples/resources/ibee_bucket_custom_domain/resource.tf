# The bucket must be public, and the domain's CNAME record must exist first: the
# API checks it when the domain is added (its refusal names the target).
resource "ibee_bucket_custom_domain" "assets" {
  bucket_name = "my-assets"
  domain      = "assets.example.com"
}

output "cname_target" {
  value = ibee_bucket_custom_domain.assets.cname_target
}
