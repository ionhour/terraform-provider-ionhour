# Look a project up by exact name ...
data "ionhour_project" "payments" {
  name = "Payments"
}

# ... or by id.
data "ionhour_project" "by_id" {
  id = 7
}
