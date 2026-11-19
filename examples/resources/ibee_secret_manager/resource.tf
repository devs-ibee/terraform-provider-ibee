# Turns on Secret Manager for the workspace. The namespace can't change later,
# and destroy leaves Secret Manager on.
resource "ibee_secret_manager" "this" {
  namespace = "team-billing"
}

# Write and read secret values with the Vault API at this address.
output "vault_endpoint" {
  value = ibee_secret_manager.this.endpoint
}
