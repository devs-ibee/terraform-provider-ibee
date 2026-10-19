# Let an identity also read a second store.
resource "ibee_secret_identity_scope" "shared" {
  identity_id        = "IDENTITY_ID"
  store_id           = "SHARED_STORE_ID"
  access_mode        = "read_only"
  allow_version_read = true
}
