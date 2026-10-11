data "ionhour_regions" "all" {}

# Probe from every enabled region (the plan caps the count).
resource "ionhour_check" "everywhere" {
  name       = "Homepage from everywhere"
  project_id = ionhour_project.payments.id
  config = {
    url     = "https://www.example.com/"
    regions = slice(data.ionhour_regions.all.ids, 0, min(3, length(data.ionhour_regions.all.ids)))
  }
}
