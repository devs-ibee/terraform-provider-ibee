# Settings and tags of one secret. Terraform never handles the value: write it
# with the Vault API, for example `bao kv put`.
resource "ibee_secret_manager_secret" "database" {
  path            = "apps/billing/db"
  custom_metadata = { owner = "billing" }
  max_versions    = 10
  cas_required    = true

  # Destroy permanently deletes the secret and every version of its value.
  lifecycle {
    prevent_destroy = true
  }
}
