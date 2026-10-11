resource "ionhour_check" "public_api" {
  name             = "Public API health"
  slug             = "public-api"
  project_id       = ionhour_project.payments.id
  interval_seconds = 60
  down_severity    = "P1"

  config = {
    url                 = "https://api.example.com/health"
    method              = "GET"
    expected_status_min = 200
    expected_status_max = 299
    regions             = ["eu-central-1", "us-east-1"]
    custom_headers = {
      "x-health-token" = var.health_token
    }
    body_contains = "\"status\":\"ok\""
  }
}

variable "health_token" {
  type      = string
  sensitive = true
}
