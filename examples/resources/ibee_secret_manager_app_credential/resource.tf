# An AppRole login for an application. The secret ID is issued once, kept in
# state (sensitive) and not recovered by import. Destroy revokes the credential.
resource "ibee_secret_manager_app_credential" "billing" {
  name   = "billing-app"
  access = "read"
  folder = "apps/billing"
}

output "role_id" {
  value = ibee_secret_manager_app_credential.billing.role_id
}

output "secret_id" {
  value     = ibee_secret_manager_app_credential.billing.secret_id
  sensitive = true
}
