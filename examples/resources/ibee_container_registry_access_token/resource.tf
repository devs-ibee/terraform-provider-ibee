resource "ibee_container_registry_access_token" "ci" {
  registry_id     = "REGISTRY_ID"
  name            = "ci-push"
  permission      = "PULL_PUSH"
  expires_in_days = 90
}

# docker login registry.ibee.ai -u "$(terraform output -raw registry_username)"
output "registry_username" {
  value = ibee_container_registry_access_token.ci.username
}
