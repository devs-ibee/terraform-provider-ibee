resource "ibee_cdn_distribution" "assets" {
  name        = "assets"
  origin_type = "bucket"
  origin_id   = "my-assets"
}
