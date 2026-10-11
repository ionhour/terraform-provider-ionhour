# Import a job by its numeric id ...
terraform import ionhour_job.nightly_backup 456

# ... or by its slug.
terraform import ionhour_job.nightly_backup slug:nightly-backup

# A job auto-provisioned by a slug-mode SDK ping depends on its slug. Put the
# same slug in configuration BEFORE importing it, for example
#   slug = "nightly-backup"
# otherwise the next apply clears the slug and slug-mode pings
# (/signals/s/nightly-backup) stop resolving.
#
# Legacy jobs created from an unparseable schedule have no interval: they
# import with interval_seconds = null, and the next apply writes 3600 (or the
# value in configuration).
