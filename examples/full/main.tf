# A complete module: one project, two HTTP checks and one heartbeat job.
#
#   export IONHOUR_API_KEY=ionh_...            # a SERVICE key is recommended
#   export IONHOUR_API_BASE_URL=https://...    # only for SIT/UAT
#   terraform init && terraform apply

terraform {
  required_version = ">= 1.5"

  required_providers {
    ionhour = {
      source  = "ionhour/ionhour"
      version = "~> 0.1"
    }
  }
}

provider "ionhour" {}

variable "environment" {
  type    = string
  default = "production"
}

resource "ionhour_project" "storefront" {
  name        = "Storefront (${var.environment})"
  description = "Managed by Terraform"
}

# Defaults: GET, every 300 s, 200-399 passes, environment default regions.
resource "ionhour_check" "homepage" {
  name       = "Homepage"
  slug       = "storefront-homepage-${var.environment}"
  project_id = ionhour_project.storefront.id

  config = {
    url = "https://www.example.com/"
  }
}

resource "ionhour_check" "checkout_api" {
  name             = "Checkout API"
  slug             = "storefront-checkout-${var.environment}"
  project_id       = ionhour_project.storefront.id
  interval_seconds = 60
  grace_seconds    = 10
  down_severity    = "P1"
  late_severity    = "P2"

  config = {
    url                    = "https://api.example.com/checkout/health"
    method                 = "GET"
    timeout_ms             = 5000
    expected_status_min    = 200
    expected_status_max    = 299
    follow_redirects       = false
    fail_after_consecutive = 2
    body_contains          = "ok"
  }
}

resource "ionhour_job" "order_export" {
  name             = "Nightly order export"
  slug             = "storefront-order-export-${var.environment}"
  project_id       = ionhour_project.storefront.id
  interval_seconds = 86400
  grace_seconds    = 3600
}

output "check_ids" {
  value = {
    homepage     = ionhour_check.homepage.id
    checkout_api = ionhour_check.checkout_api.id
  }
}

# The ping URL is a credential: anyone holding it can report the job healthy.
output "order_export_ping_url" {
  value     = ionhour_job.order_export.ping_url
  sensitive = true
}
