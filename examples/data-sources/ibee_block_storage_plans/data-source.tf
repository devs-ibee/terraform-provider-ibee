data "ibee_block_storage_plans" "all" {
  site_id = "SITE_ID"
}

output "block_storage_sku_codes" {
  value = data.ibee_block_storage_plans.all.plans[*].sku_code
}
