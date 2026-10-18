variable "database_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "ibee_secret" "database" {
  store_id         = "STORE_ID"
  secret_name      = "database-password"
  value_wo         = jsonencode({ password = var.database_password })
  value_wo_version = 1
}
