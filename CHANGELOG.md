# Changelog

All notable changes to the Ionhour Terraform provider (`ionhour/ionhour`).
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/).

## 0.1.0

Initial release, built on the Terraform Plugin Framework (protocol 6) and the
Integration API v1.

### Provider

- `api_key` (sensitive, `IONHOUR_API_KEY`) and `base_url`
  (`IONHOUR_API_BASE_URL`, default `https://api.ionhour.com/api`).
- Validates the key with `GET /v1/whoami` at configure time and logs the
  workspace at INFO. An invalid, expired or inactive key fails the run with
  the API error code.
- Rate limits: a 429 waits its `Retry-After` plus up to 1 s of jitter and
  is retried until a 5-minute budget per API call is spent, so batch applies
  with parallel resources survive a 60 requests/minute plan. 5xx and network
  failures are retried once. Every create carries a fresh `Idempotency-Key`,
  so retries never duplicate a resource.

### Resources

- `ionhour_project`: name and description. A delete fails with
  `PROJECT_NOT_EMPTY` while the project still holds monitors. Imports by
  numeric id.
- `ionhour_check`: outbound HTTP check with a `config` object for url, method,
  custom headers, timeout, expected status range, regions, redirects,
  consecutive-failure thresholds, latency and TLS-expiry alerts, and body
  assertions. `config.url` forces replacement, and `project_id` moves the
  check in place. Imports by numeric id or `slug:<slug>`. HTTP checks only:
  reading or importing another check type fails with an error.
- `ionhour_job`: heartbeat job with interval, grace, severities and an
  optional slug. Exposes `ping_token` and `ping_url` as sensitive attributes.
  `project_id` moves the job in place and keeps its token. Imports by numeric
  id or `slug:<slug>`; set `slug` in configuration before importing an
  auto-provisioned job. Legacy jobs without an interval import as null and
  the next apply writes 3600.

### Data sources

- `ionhour_regions`: the probe regions the environment serves.
- `ionhour_project`: lookup by id or exact name.
- `ionhour_check`: lookup by id or slug.
- `ionhour_job`: lookup by id or slug.

### Behaviour

- Server defaults are mirrored as schema defaults, so unset attributes never
  produce a diff.
- Removing an attribute clears it or resets it to its default.
- Custom header names are sent lowercased. The configured spelling is kept in
  state.
- Resources deleted outside Terraform are recreated on the next apply.
