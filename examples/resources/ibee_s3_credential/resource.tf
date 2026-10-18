resource "ibee_s3_credential" "reader" {
  name            = "assets-reader"
  permission_type = "object_ro"
  bucket_scope    = "specific"
  allowed_buckets = ["my-assets"]
}
