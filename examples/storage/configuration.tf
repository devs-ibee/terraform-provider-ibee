# Optional advanced configuration. Defaults create none of these resources.
variable "manage_bucket_configuration" {
  type    = bool
  default = false
}
resource "ibee_bucket_cors" "assets" {
  count       = var.manage_bucket_configuration ? 1 : 0
  bucket_name = ibee_bucket.assets.name
  rules = [{
    allowed_origins = ["https://app.example.com"]
    allowed_methods = ["GET", "HEAD"]
    max_age_seconds = 3600
  }]
}
resource "ibee_bucket_lifecycle" "assets" {
  count       = var.manage_bucket_configuration ? 1 : 0
  bucket_name = ibee_bucket.assets.name
  rules = [{
    status          = "Disabled"
    prefix          = "temporary/"
    expiration_days = 30
  }]
}
# Readback confirms stored CORS rules, not direct S3 endpoint enforcement.
# Enable expiration only when deletion of matching objects is intended.

variable "create_scoped_s3_credential" {
  type    = bool
  default = false
}
resource "ibee_s3_credential" "reader" {
  count           = var.create_scoped_s3_credential ? 1 : 0
  name            = "terraform-assets-reader"
  permission_type = "object_ro"
  bucket_scope    = "specific"
  allowed_buckets = [ibee_bucket.assets.name]
}
# The creation-only S3 secret is sensitive but is persisted in Terraform state.
# Use an encrypted, access-controlled backend; imported keys cannot reveal it.
