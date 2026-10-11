# Terraform provider for Ionhour

`terraform-provider-ionhour` manages Ionhour projects, outbound HTTP checks and
heartbeat jobs through the Integration API v1. It is built on the
[Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework)
and the generated Go client in [`../go/api`](../go/api).

```hcl
terraform {
  required_providers {
    ionhour = {
      source = "ionhour/ionhour"
    }
  }
}

provider "ionhour" {} # IONHOUR_API_KEY, optional IONHOUR_API_BASE_URL

resource "ionhour_project" "storefront" {
  name = "Storefront"
}

resource "ionhour_check" "homepage" {
  name       = "Homepage"
  slug       = "storefront-homepage"
  project_id = ionhour_project.storefront.id
  config = {
    url = "https://www.example.com/"
  }
}
```

A full module with a project, two checks and a job is in
[`examples/full/main.tf`](./examples/full/main.tf). Registry docs are in
[`docs/`](./docs).

## Source of truth and release

This directory in the Ionhour monorepo is the source of truth. On release it
is mirrored to the public repository
[`github.com/ionhour/terraform-provider-ionhour`](https://github.com/ionhour/terraform-provider-ionhour),
which the Terraform Registry reads. Releases are cut there with goreleaser
(`.goreleaser.yml`): GPG-signed `SHA256SUMS`, the
`terraform-registry-manifest.json` (protocol 6.0) and zipped binaries in the
standard registry layout. Never edit the mirror directly.

Pushing a `v*` tag in the mirror runs `.github/workflows/release.yml`. It
fails fast while the `replace` directive below is present, then imports the
signing key from the `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets and runs
`goreleaser release --clean`. Record each release under a new numbered
heading in `CHANGELOG.md`; the mirror workflow reads the first one.

### The `replace` directive

`go.mod` builds against the in-repo client through

```
replace github.com/ionhour/go-sdk/api => ../go/api
```

because the `github.com/ionhour/go-sdk` mirror has no `api/vX.Y.Z` tag yet.
Before the first release from the public mirror, drop the directive and pin
the tag:

```bash
go mod edit -dropreplace=github.com/ionhour/go-sdk/api
go get github.com/ionhour/go-sdk/api@api/v0.1.0
```

The goreleaser `before` hook fails the release while the directive is
present.

## Provider configuration

| Attribute | Environment | Default |
|---|---|---|
| `api_key` (sensitive) | `IONHOUR_API_KEY` | none, required |
| `base_url` | `IONHOUR_API_BASE_URL` | `https://api.ionhour.com/api` |

The provider calls `GET /v1/whoami` when it is configured. An invalid,
expired or inactive key fails the run with a diagnostic carrying the API code.
On success it logs the workspace id, name and role at INFO (`TF_LOG=INFO`)
instead of printing a warning on every plan.

Rate limits: a 429 waits its `Retry-After` plus up to 1 s of jitter and is
retried until a 5-minute budget per API call is spent or the run is cancelled,
so batch applies with parallel resources survive a 60 requests/minute plan.
A 5xx or network failure is retried once after 1 s. Creates carry a fresh
`Idempotency-Key`, so a retried create never duplicates.

## Behaviour

- **No perpetual diffs.** Every server default is an Optional+Computed schema
  default with the same value. Server-computed values that never change (ids,
  `created_at`, the read-only probe settings, the ping token, `regions` when
  unset) keep their prior value in plans; `status`, `paused` and `updated_at`
  show as known after apply on updates.
- **Explicit nulls.** Removing a nullable attribute sends `null` and clears
  it. Removing a defaulted `config` attribute sends `null`, and the server
  resets it to the default. Removing a defaulted top-level attribute
  restates its default.
- **Header names.** `config.custom_headers` names are lowercased on the wire.
  The configured spelling is kept in state when the server's map is the
  lowercased equivalent.
- **Immutable URL.** `config.url` and `check_type` force replacement.
  `project_id` moves a check or job in place.
- **Import.** Checks and jobs import by numeric id or `slug:<slug>`. Projects
  import by numeric id. Set `slug` in configuration before importing an
  auto-provisioned job, or the next apply clears it and slug-mode pings stop
  resolving. Legacy jobs without an interval import as null; the next apply
  writes 3600.
- **HTTP checks only (v0.1).** Reading or importing a non-HTTP check fails
  with an error rather than planning a replacement.
- **404s.** A resource deleted outside Terraform is dropped from state on
  refresh and planned for creation. Deleting an already-deleted resource
  succeeds.
- **Secrets in state.** `ionhour_job.ping_token`, `ping_url` and
  `config.custom_headers` are sensitive but stored in state in plain text.
  Use an encrypted backend. Reading the ping token back needs a READ_WRITE key
  with `mcp:jobs` (or `mcp:signals`) and MEMBER role or higher; otherwise it is
  null.

## Development

Go is not required locally. Every command runs in Docker with the repository
mounted at `/src`, so the `replace` directive resolves. Run them from the
repository root.

```bash
GO="docker run --rm -v $PWD:/src -w /src/sdks/terraform-provider \
  -v ionhour-go-mod:/go/pkg/mod -e GOFLAGS=-modcacherw golang:1.25"

# Format, vet and unit tests (acceptance tests skip without TF_ACC)
$GO sh -c "gofmt -l . && go vet ./... && go test -race ./..."
```

The framework and testing libraries need Go 1.25, so the image is
`golang:1.25` rather than the `golang:1.22` the `api` module uses.

### Acceptance tests

Acceptance tests run real Terraform against the provider. They need a
Terraform CLI inside the container. Install it once into a volume:

```bash
docker run --rm -v ionhour-terraform-bin:/tf golang:1.25 sh -c '
  apt-get update -qq && apt-get install -y -qq unzip >/dev/null &&
  curl -fsSLo /tmp/tf.zip https://releases.hashicorp.com/terraform/1.16.5/terraform_1.16.5_linux_$(dpkg --print-architecture).zip &&
  unzip -o -q /tmp/tf.zip -d /tf'
```

Then run the suite against the in-process contract fake
(`internal/provider/fakeapi_test.go`):

```bash
docker run --rm -v $PWD:/src -w /src/sdks/terraform-provider \
  -v ionhour-go-mod:/go/pkg/mod -v ionhour-terraform-bin:/tf \
  -e GOFLAGS=-modcacherw -e TF_ACC=1 -e TF_ACC_TERRAFORM_PATH=/tf/terraform \
  golang:1.25 go test -race -count=1 ./...
```

Every resource suite (`TestAccProjectResource`, `TestAccCheckResource`,
`TestAccJobResource`, `TestAccDataSources`) also has a `live` variant. It
runs the same steps against a real API when both variables are set:

| Variable | Value |
|---|---|
| `IONHOUR_ACC_BASE_URL` | e.g. the SIT API base URL, including `/api` |
| `IONHOUR_ACC_API_KEY` | a **SERVICE** key in a dedicated SIT workspace |

The key needs `serviceRole` `MEMBER` (for create and update) or `ADMIN` (the
suites also destroy), READ_WRITE access, and `mcp:projects`, `mcp:checks` and
`mcp:jobs`. The workspace plan must allow the plan-gated check fields the
suite sets (`custom_headers`, `latency_warn_ms`, `body_contains`). Never point
it at a workspace with real monitors. Run it nightly and before a release, not
per pull request.

### Docs

`docs/` is generated by
[`tfplugindocs`](https://github.com/hashicorp/terraform-plugin-docs) from the
schema descriptions, `examples/` and `templates/`. Regenerate it after a
schema change:

```bash
docker run --rm -v $PWD:/src -w /src/sdks/terraform-provider \
  -v ionhour-go-mod:/go/pkg/mod -v ionhour-terraform-bin:/tf \
  -e GOFLAGS=-modcacherw -e PATH=/tf:/usr/local/go/bin:/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  golang:1.25 sh -c "go generate ./... && go tool tfplugindocs validate --provider-name ionhour"
```
