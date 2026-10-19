action "ibee_cdn_verify_domain" "www" {
  config {
    distribution_id = "DISTRIBUTION_ID"
    domain          = "www.example.com"
  }
}

# terraform apply -invoke=action.ibee_cdn_verify_domain.www
