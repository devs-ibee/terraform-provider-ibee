terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "bucket_name" {
  description = "Existing Object Lock-enabled bucket. Its default retention configuration cannot currently be cleared."
  type        = string
}

variable "enable_default_retention" {
  description = "Opt in only after reviewing the lasting retention policy and its effects on protected objects."
  type        = bool
  default     = false
}

resource "ibee_bucket_retention" "default" {
  count       = var.enable_default_retention ? 1 : 0
  bucket_name = var.bucket_name
  mode        = "GOVERNANCE"
  days        = 30

  # The API cannot clear this policy. Before destroy, explicitly change this
  # to true and apply; Terraform will then relinquish management but the remote
  # retention rule will remain active.
  retain_on_destroy = false
}

output "retention_bucket" {
  value = var.enable_default_retention ? ibee_bucket_retention.default[0].bucket_name : null
}
