terraform {
  required_providers {
    ionhour = {
      source = "ionhour/ionhour"
    }
  }
}

# Credentials come from IONHOUR_API_KEY (and IONHOUR_API_BASE_URL for SIT/UAT)
# when the attributes are omitted.
provider "ionhour" {
  api_key = var.ionhour_api_key
}

variable "ionhour_api_key" {
  type      = string
  sensitive = true
}
