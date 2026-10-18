resource "ibee_block_volume" "data" {
  name     = "data"
  site_id  = "SITE_ID"
  sku_code = "BLOCK_STORAGE_SKU"
  size_gb  = 100
}
