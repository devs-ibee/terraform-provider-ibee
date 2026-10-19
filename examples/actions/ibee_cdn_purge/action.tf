action "ibee_cdn_purge" "home_page" {
  config {
    distribution_id = "DISTRIBUTION_ID"
    mode            = "url"
    paths           = ["/index.html"]
  }
}

# terraform apply -invoke=action.ibee_cdn_purge.home_page
