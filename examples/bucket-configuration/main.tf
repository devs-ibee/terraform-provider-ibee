terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = {
      source = "devs-ibee/ibee"
    }
  }
}

# Set IBEE_TOKEN, IBEE_WORKSPACE_ID and optionally IBEE_ENDPOINT.
provider "ibee" {}

variable "bucket_name" {
  description = "Existing bucket name. Import each configuration section first if it already contains entries."
  type        = string
}

variable "application_origin" {
  description = "HTTP(S) application origin allowed by the stored CORS policy."
  type        = string
}

variable "notification_webhook_url" {
  description = "HTTP(S) destination for bucket events. Verify event delivery separately."
  type        = string
}

resource "ibee_bucket_cors" "application" {
  bucket_name = var.bucket_name
  rules = [{
    id              = "application"
    allowed_origins = [var.application_origin]
    allowed_methods = ["GET", "HEAD"]
    allowed_headers = ["Authorization"]
    expose_headers  = ["ETag"]
    max_age_seconds = 300
  }]
}

# This example stores a disabled expiration rule. Enabling expiration can
# permanently delete matching objects. Stop writers before changing retention.
resource "ibee_bucket_lifecycle" "archive" {
  bucket_name = var.bucket_name
  rules = [{
    status          = "Disabled"
    prefix          = "archive/"
    expiration_days = 90
  }]
}

resource "ibee_bucket_notifications" "application" {
  bucket_name = var.bucket_name
  configs = [{
    id          = "application-events"
    events      = ["upload", "delete"]
    webhook_url = var.notification_webhook_url
    filter = {
      prefix = "uploads/"
      suffix = ".json"
    }
  }]
}

output "notification_s3_events" {
  value = ibee_bucket_notifications.application.configs[0].s3_events
}

# Each resource owns its entire section. Destroy clears that section. CORS is
# stored metadata with best-effort public URL/CDN synchronization; MinIO does
# not enforce these rules directly. Test browser behavior and webhook delivery
# in the target environment after applying.
