resource "ionhour_job" "nightly_backup" {
  name             = "Nightly backup"
  slug             = "nightly-backup"
  project_id       = ionhour_project.payments.id
  interval_seconds = 86400
  grace_seconds    = 1800
}

# Hand the ping URL to the system that runs the job, e.g. a secret store.
output "nightly_backup_ping_url" {
  value     = ionhour_job.nightly_backup.ping_url
  sensitive = true
}
