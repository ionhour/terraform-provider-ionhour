data "ionhour_job" "nightly_backup" {
  slug = "nightly-backup"
}

output "nightly_backup_ping_url" {
  value     = data.ionhour_job.nightly_backup.ping_url
  sensitive = true
}
