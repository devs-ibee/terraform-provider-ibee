resource "ibee_cdn_origin" "app" {
  name       = "app"
  origin_url = "https://origin.example.com"
}
