resource "ibee_container_registry" "apps" {
  name                      = "acme-apps"
  sku_code                  = "REGISTRY-STORAGE-250GB"
  visibility                = "PRIVATE"
  vulnerability_pull_policy = "BLOCK_CRITICAL"
}
