terraform {
  required_version = ">= 1.14.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}
provider "ibee" {}
variable "distribution_id" {
  type = string
}
variable "domain" {
  type = string
}
action "ibee_cdn_purge" "selected_paths" {
  config {
    distribution_id = var.distribution_id
    mode            = "url"
    paths           = ["/index.html"]
  }
}
action "ibee_cdn_verify_domain" "verify" {
  config {
    distribution_id = var.distribution_id
    domain          = var.domain
  }
}
# Actions run only when explicitly invoked or bound to an action trigger.
# terraform apply -invoke=action.ibee_cdn_purge.selected_paths
# terraform apply -invoke=action.ibee_cdn_verify_domain.verify

variable "vm_id" {
  type = string
}
action "ibee_vm_power" "start" {
  config {
    vm_id     = var.vm_id
    vm_type   = "cloud"
    operation = "start"
  }
}
# terraform apply -invoke=action.ibee_vm_power.start
# Other explicit power operations are stop and reboot; vm_type may be gpu.
