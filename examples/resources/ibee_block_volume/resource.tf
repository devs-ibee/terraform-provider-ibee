data "ibee_block_storage_plans" "all" {
  site_id = "SITE_ID"
}

resource "ibee_block_volume" "data" {
  name     = "data"
  site_id  = "SITE_ID"
  sku_code = data.ibee_block_storage_plans.all.plans[0].sku_code
  size_gb  = 100
}
