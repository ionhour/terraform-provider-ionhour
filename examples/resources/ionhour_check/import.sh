# Import a check by its numeric id ...
terraform import ionhour_check.public_api 123

# ... or by its slug.
terraform import ionhour_check.public_api slug:public-api

# Only HTTP checks can be imported in v0.1. Importing a TCP, DNS, SSL or
# heartbeat check fails; read those with the ionhour_check data source.
