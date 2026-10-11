data "ionhour_check" "public_api" {
  slug = "public-api"
}

output "public_api_status" {
  value = data.ionhour_check.public_api.status
}
