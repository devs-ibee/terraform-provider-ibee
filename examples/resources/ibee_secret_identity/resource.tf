resource "ibee_secret_identity" "billing" {
  store_id          = "STORE_ID"
  name              = "billing-service"
  auth_method       = "approle"
  token_policy_mode = "read_only"
}

# role_id and secret_id are read once, at creation.
output "billing_role_id" {
  value = ibee_secret_identity.billing.role_id
}

output "billing_secret_id" {
  value     = ibee_secret_identity.billing.secret_id
  sensitive = true
}
