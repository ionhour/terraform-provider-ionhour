---
page_title: "Getting started"
subcategory: ""
description: |-
  Manage an Ionhour project, two HTTP checks and a heartbeat job with Terraform.
---

# Getting started

## Credentials

Create a workspace-owned **SERVICE** API key in Workspace settings > API tokens:

- `serviceRole` `MEMBER` covers create and update. Deleting checks, jobs and projects needs `ADMIN`.
- Allow the `mcp:projects`, `mcp:checks` and `mcp:jobs` resources with READ_WRITE access.
  Without `mcp:jobs` write access, `ionhour_job.ping_token` and `ping_url` read back as null.

Export it before running Terraform:

```shell
export IONHOUR_API_KEY=ionh_...
# SIT / UAT only:
export IONHOUR_API_BASE_URL=https://api.sit.example/api
```

## A complete module

```terraform
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
```

## Behaviour worth knowing

- **Defaults never drift.** Every attribute the API defaults has the same default in the schema, so an
  unset attribute plans exactly what the server stores.
- **Removing an attribute resets it.** Nullable attributes (`slug`, `description`,
  `config.custom_headers`, `config.latency_warn_ms`, `config.body_contains`, ...) are cleared with an
  explicit null. Defaulted ones go back to their default.
- **`config.url` is immutable.** Changing it replaces the check, which gives it a new id and a fresh history.
- **Moving between projects is in place.** Changing `project_id` keeps the id, history and ping token.
- **Project deletes never cascade.** Destroying a project that still holds monitors, including ones
  created in the dashboard, fails with `PROJECT_NOT_EMPTY`.
- **Creates are idempotent.** Each create carries a fresh `Idempotency-Key`, so a retried request never
  creates a duplicate.
- **State holds secrets.** `ping_token`, `ping_url` and `config.custom_headers` are stored in state in
  plain text. Use an encrypted remote backend.
