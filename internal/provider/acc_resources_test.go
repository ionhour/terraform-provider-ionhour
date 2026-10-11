package provider

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Attributes the server may change between apply and import (probe results).
var volatileAttrs = []string{"status", "paused", "updated_at"}

// ---- ionhour_project ----

func TestAccProjectResource(t *testing.T) { runAcc(t, projectSteps) }

func projectSteps(_ *testing.T, env accEnv) []resource.TestStep {
	const res = "ionhour_project.p"
	name := env.name("project")
	return []resource.TestStep{
		{
			Config: fmt.Sprintf(`
resource "ionhour_project" "p" {
  name        = %q
  description = "managed by acceptance tests"
}`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "name", name),
				resource.TestCheckResourceAttr(res, "description", "managed by acceptance tests"),
				resource.TestCheckResourceAttrSet(res, "id"),
				resource.TestCheckResourceAttrSet(res, "created_at"),
				resource.TestCheckResourceAttrSet(res, "updated_at"),
				env.onFake(expectIdempotencyKeys("/api/v1/projects")),
				env.onFake(expectLastBody("POST", "/api/v1/projects", fmt.Sprintf(`{"name":%q,"description":"managed by acceptance tests"}`, name))),
			),
		},
		{
			// Rename and clear the description: explicit null on the wire.
			Config: fmt.Sprintf(`
resource "ionhour_project" "p" {
  name = "%s-renamed"
}`, name),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "name", name+"-renamed"),
				resource.TestCheckNoResourceAttr(res, "description"),
				env.onFake(expectLastBody("PATCH", "/api/v1/projects/", fmt.Sprintf(`{"name":"%s-renamed","description":null}`, name))),
			),
		},
		{
			ResourceName:            res,
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: []string{"updated_at"},
		},
	}
}

// ---- ionhour_check ----

func TestAccCheckResource(t *testing.T) { runAcc(t, checkSteps) }

func checkSteps(_ *testing.T, env accEnv) []resource.TestStep {
	const res = "ionhour_check.c"
	slug := env.name("check")
	project := fmt.Sprintf(`
resource "ionhour_project" "p" {
  name = %q
}
`, env.name("check-project"))
	var firstID string

	return []resource.TestStep{
		{
			// Create with plan-gated optional fields and a mixed-case header
			// name; every server default must plan without a diff.
			Config: project + fmt.Sprintf(`
resource "ionhour_check" "c" {
  name       = "acc check %[1]s"
  slug       = %[1]q
  project_id = ionhour_project.p.id
  config = {
    url             = "https://example.com/"
    custom_headers  = { "X-Acc-Token" = "secret-1" }
    latency_warn_ms = 2000
    body_contains   = "Example"
  }
}`, slug),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "slug", slug),
				resource.TestCheckResourceAttr(res, "check_type", "HTTP"),
				resource.TestCheckResourceAttr(res, "interval_seconds", "300"),
				resource.TestCheckResourceAttr(res, "grace_seconds", "5"),
				resource.TestCheckResourceAttr(res, "mute_notifications", "false"),
				resource.TestCheckResourceAttr(res, "down_severity", "P1"),
				resource.TestCheckResourceAttr(res, "late_severity", "P3"),
				resource.TestCheckResourceAttr(res, "config.method", "GET"),
				resource.TestCheckResourceAttr(res, "config.custom_headers.X-Acc-Token", "secret-1"),
				resource.TestCheckResourceAttr(res, "config.timeout_ms", "8000"),
				resource.TestCheckResourceAttr(res, "config.expected_status_min", "200"),
				resource.TestCheckResourceAttr(res, "config.expected_status_max", "399"),
				resource.TestCheckResourceAttr(res, "config.follow_redirects", "true"),
				resource.TestCheckResourceAttr(res, "config.max_redirects", "3"),
				resource.TestCheckResourceAttr(res, "config.block_http_downgrade", "true"),
				resource.TestCheckResourceAttr(res, "config.fail_after_consecutive", "3"),
				resource.TestCheckResourceAttr(res, "config.resolve_after_consecutive_success", "2"),
				resource.TestCheckResourceAttr(res, "config.ssl_expiry_warn_days", "14"),
				resource.TestCheckResourceAttr(res, "config.latency_warn_ms", "2000"),
				resource.TestCheckResourceAttr(res, "config.body_contains", "Example"),
				resource.TestCheckNoResourceAttr(res, "config.body_not_contains"),
				resource.TestCheckResourceAttrSet(res, "config.regions.#"),
				resource.TestCheckResourceAttrSet(res, "config.ip_family"),
				resource.TestCheckResourceAttrSet(res, "config.max_response_bytes"),
				resource.TestCheckResourceAttrSet(res, "status"),
				resource.TestCheckResourceAttrPair(res, "project_id", "ionhour_project.p", "id"),
				captureAttr(res, "id", &firstID),
				env.onFake(expectIdempotencyKeys("/api/v1/checks")),
				// Only configured attributes are sent; header names lowercased.
				env.onFake(expectLastBody("POST", "/api/v1/checks", fmt.Sprintf(`{
					"name":"acc check %[1]s","slug":%[1]q,
					"config":{"url":"https://example.com/","customHeaders":{"x-acc-token":"secret-1"},
						"latencyWarnMs":2000,"bodyContains":"Example"}}`, slug), "projectId")),
			),
		},
		{
			// In-place update: only changed keys go out, never config.url.
			Config: project + fmt.Sprintf(`
resource "ionhour_check" "c" {
  name             = "acc check %[1]s v2"
  slug             = %[1]q
  project_id       = ionhour_project.p.id
  interval_seconds = 600
  config = {
    url             = "https://example.com/"
    method          = "HEAD"
    custom_headers  = { "x-acc-token" = "secret-2" }
    timeout_ms      = 5000
    latency_warn_ms = 2000
    body_contains   = "Example"
  }
}`, slug),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "interval_seconds", "600"),
				resource.TestCheckResourceAttr(res, "config.method", "HEAD"),
				resource.TestCheckResourceAttr(res, "config.timeout_ms", "5000"),
				resource.TestCheckResourceAttr(res, "config.custom_headers.x-acc-token", "secret-2"),
				expectAttrUnchanged(res, "id", &firstID),
				env.onFake(expectLastBody("PATCH", "/api/v1/checks/", fmt.Sprintf(`{
					"name":"acc check %s v2","intervalSeconds":600,
					"config":{"method":"HEAD","customHeaders":{"x-acc-token":"secret-2"},"timeoutMs":5000}}`, slug))),
			),
		},
		{
			ResourceName:            res,
			ImportState:             true,
			ImportStateIdFunc:       importBySlug(slug),
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: volatileAttrs,
		},
		{
			// Clear every optional field: nullable ones become null, defaulted
			// config ones reset via null, top-level ones restate the default.
			Config: project + fmt.Sprintf(`
resource "ionhour_check" "c" {
  name       = "acc check %s v2"
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.com/"
  }
}`, slug),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(res, "slug"),
				resource.TestCheckNoResourceAttr(res, "config.custom_headers.%"),
				resource.TestCheckNoResourceAttr(res, "config.latency_warn_ms"),
				resource.TestCheckNoResourceAttr(res, "config.body_contains"),
				resource.TestCheckResourceAttr(res, "config.method", "GET"),
				resource.TestCheckResourceAttr(res, "config.timeout_ms", "8000"),
				resource.TestCheckResourceAttr(res, "interval_seconds", "300"),
				env.onFake(expectLastBody("PATCH", "/api/v1/checks/", `{
					"slug":null,"intervalSeconds":300,
					"config":{"method":null,"customHeaders":null,"timeoutMs":null,"latencyWarnMs":null,"bodyContains":null}}`)),
			),
		},
		{
			ResourceName:            res,
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: volatileAttrs,
		},
		{
			// config.url is immutable: the check is replaced.
			Config: project + fmt.Sprintf(`
resource "ionhour_check" "c" {
  name       = "acc check %s v2"
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.org/"
  }
}`, slug),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionReplace)},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "config.url", "https://example.org/"),
				expectAttrChanged(res, "id", &firstID),
			),
		},
	}
}

// ---- ionhour_job ----

func TestAccJobResource(t *testing.T) { runAcc(t, jobSteps) }

func jobSteps(_ *testing.T, env accEnv) []resource.TestStep {
	const res = "ionhour_job.j"
	slug := env.name("job")
	projects := fmt.Sprintf(`
resource "ionhour_project" "a" {
  name = %q
}

resource "ionhour_project" "b" {
  name = %q
}
`, env.name("job-a"), env.name("job-b"))
	var firstID, firstToken string

	return []resource.TestStep{
		{
			Config: projects + fmt.Sprintf(`
resource "ionhour_job" "j" {
  name             = "acc job %[1]s"
  slug             = %[1]q
  project_id       = ionhour_project.a.id
  interval_seconds = 86400
  grace_seconds    = 600
}`, slug),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "slug", slug),
				resource.TestCheckResourceAttr(res, "interval_seconds", "86400"),
				resource.TestCheckResourceAttr(res, "grace_seconds", "600"),
				resource.TestCheckResourceAttr(res, "down_severity", "P1"),
				resource.TestCheckResourceAttr(res, "late_severity", "P3"),
				resource.TestCheckResourceAttr(res, "mute_notifications", "false"),
				resource.TestCheckResourceAttrSet(res, "ping_token"),
				resource.TestCheckResourceAttrSet(res, "ping_url"),
				resource.TestCheckResourceAttrPair(res, "project_id", "ionhour_project.a", "id"),
				captureAttr(res, "id", &firstID),
				captureAttr(res, "ping_token", &firstToken),
				env.onFake(expectIdempotencyKeys("/api/v1/jobs")),
				env.onFake(expectLastBody("POST", "/api/v1/jobs", fmt.Sprintf(`{
					"name":"acc job %[1]s","slug":%[1]q,"intervalSeconds":86400,"graceSeconds":600}`, slug), "projectId")),
			),
		},
		{
			// Moving projects is an in-place update: same id, same token.
			Config: projects + fmt.Sprintf(`
resource "ionhour_job" "j" {
  name             = "acc job %[1]s"
  slug             = %[1]q
  project_id       = ionhour_project.b.id
  interval_seconds = 7200
  grace_seconds    = 600
  late_severity    = "P2"
}`, slug),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(res, "project_id", "ionhour_project.b", "id"),
				resource.TestCheckResourceAttr(res, "interval_seconds", "7200"),
				resource.TestCheckResourceAttr(res, "late_severity", "P2"),
				expectAttrUnchanged(res, "id", &firstID),
				expectAttrUnchanged(res, "ping_token", &firstToken),
				env.onFake(expectLastBody("PATCH", "/api/v1/jobs/", `{"intervalSeconds":7200,"lateSeverity":"P2"}`, "projectId")),
			),
		},
		{
			ResourceName:            res,
			ImportState:             true,
			ImportStateIdFunc:       importBySlug(slug),
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: volatileAttrs,
		},
		{
			// Clear the slug and fall back to the default interval and grace.
			Config: projects + fmt.Sprintf(`
resource "ionhour_job" "j" {
  name          = "acc job %s"
  project_id    = ionhour_project.b.id
  late_severity = "P2"
}`, slug),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr(res, "slug"),
				resource.TestCheckResourceAttr(res, "interval_seconds", "3600"),
				resource.TestCheckResourceAttr(res, "grace_seconds", "300"),
				env.onFake(expectLastBody("PATCH", "/api/v1/jobs/", `{"slug":null,"intervalSeconds":3600,"graceSeconds":300}`)),
			),
		},
		{
			ResourceName:            res,
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: volatileAttrs,
		},
	}
}

// ---- data sources ----

func TestAccDataSources(t *testing.T) { runAcc(t, dataSourceSteps) }

func dataSourceSteps(_ *testing.T, env accEnv) []resource.TestStep {
	return []resource.TestStep{
		{
			Config: fmt.Sprintf(`
resource "ionhour_project" "p" {
  name        = %[1]q
  description = "lookup target"
}

resource "ionhour_check" "c" {
  name       = "acc lookup check"
  slug       = %[2]q
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.com/"
  }
}

resource "ionhour_job" "j" {
  name       = "acc lookup job"
  slug       = %[3]q
  project_id = ionhour_project.p.id
}

data "ionhour_regions" "all" {}

data "ionhour_project" "by_name" {
  name = ionhour_project.p.name
}

data "ionhour_project" "by_id" {
  id = ionhour_project.p.id
}

data "ionhour_check" "by_slug" {
  slug = ionhour_check.c.slug
}

data "ionhour_check" "by_id" {
  id = ionhour_check.c.id
}

data "ionhour_job" "by_slug" {
  slug = ionhour_job.j.slug
}

data "ionhour_job" "by_id" {
  id = ionhour_job.j.id
}
`, env.name("ds-project"), env.name("ds-check"), env.name("ds-job")),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.ionhour_regions.all", "regions.#"),
				resource.TestCheckResourceAttrSet("data.ionhour_regions.all", "regions.0.id"),
				resource.TestCheckResourceAttrSet("data.ionhour_regions.all", "ids.0"),
				resource.TestCheckResourceAttrPair("data.ionhour_project.by_name", "id", "ionhour_project.p", "id"),
				resource.TestCheckResourceAttrPair("data.ionhour_project.by_name", "description", "ionhour_project.p", "description"),
				resource.TestCheckResourceAttrPair("data.ionhour_project.by_id", "name", "ionhour_project.p", "name"),
				resource.TestCheckResourceAttrPair("data.ionhour_check.by_slug", "id", "ionhour_check.c", "id"),
				resource.TestCheckResourceAttrPair("data.ionhour_check.by_slug", "config.url", "ionhour_check.c", "config.url"),
				resource.TestCheckResourceAttrPair("data.ionhour_check.by_slug", "config.timeout_ms", "ionhour_check.c", "config.timeout_ms"),
				resource.TestCheckResourceAttrPair("data.ionhour_check.by_id", "slug", "ionhour_check.c", "slug"),
				resource.TestCheckResourceAttrPair("data.ionhour_job.by_slug", "id", "ionhour_job.j", "id"),
				resource.TestCheckResourceAttrPair("data.ionhour_job.by_slug", "ping_url", "ionhour_job.j", "ping_url"),
				resource.TestCheckResourceAttrPair("data.ionhour_job.by_id", "slug", "ionhour_job.j", "slug"),
			),
		},
	}
}

// ---- fake-only scenarios (fault injection, out-of-band changes) ----

func TestAccCheckSlugTaken(t *testing.T) {
	runFakeAcc(t, func(_ *testing.T, env accEnv) []resource.TestStep {
		return []resource.TestStep{{
			Config: `
resource "ionhour_project" "p" {
  name = "slug clash"
}

resource "ionhour_job" "first" {
  name       = "first"
  slug       = "shared"
  project_id = ionhour_project.p.id
}

resource "ionhour_check" "second" {
  name       = "second"
  slug       = "shared"
  project_id = ionhour_job.first.project_id
  config = {
    url = "https://example.com/"
  }
}`,
			ExpectError: regexp.MustCompile(`SLUG_TAKEN`),
		}}
	})
}

func TestAccCheckRecreatedAfterOutOfBandDelete(t *testing.T) {
	runFakeAcc(t, func(_ *testing.T, env accEnv) []resource.TestStep {
		const res = "ionhour_check.c"
		cfg := `
resource "ionhour_project" "p" {
  name = "drift"
}

resource "ionhour_check" "c" {
  name       = "drift"
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.com/"
  }
}`
		var firstID string
		return []resource.TestStep{
			{Config: cfg, Check: captureAttr(res, "id", &firstID)},
			{
				PreConfig: func() { env.fake.deleteOutOfBand("check", atoiOrZero(firstID)) },
				Config:    cfg,
				Check:     expectAttrChanged(res, "id", &firstID),
			},
		}
	})
}

func TestAccCheckRegions(t *testing.T) {
	runFakeAcc(t, func(_ *testing.T, env accEnv) []resource.TestStep {
		const res = "ionhour_check.c"
		withRegions := func(regions string) string {
			return fmt.Sprintf(`
resource "ionhour_project" "p" {
  name = "regions"
}

resource "ionhour_check" "c" {
  name       = "regions"
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.com/"
    %s
  }
}`, regions)
		}
		return []resource.TestStep{
			{
				Config: withRegions(`regions = ["us-east-1"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "config.regions.#", "1"),
					resource.TestCheckTypeSetElemAttr(res, "config.regions.*", "us-east-1"),
				),
			},
			{
				Config: withRegions(`regions = ["eu-central-1", "ap-southeast-1"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "config.regions.#", "2"),
					env.onFake(expectLastBody("PATCH", "/api/v1/checks/", `{"config":{"regions":["ap-southeast-1","eu-central-1"]}}`)),
				),
			},
			{
				// Optional+Computed: dropping regions from config keeps them.
				Config:   withRegions(""),
				PlanOnly: true,
			},
			{
				Config:      withRegions(`regions = ["mars-1"]`),
				ExpectError: regexp.MustCompile(`not served`),
			},
		}
	})
}

func TestAccJobWithheldPingToken(t *testing.T) {
	runFakeAcc(t, func(_ *testing.T, env accEnv) []resource.TestStep {
		env.fake.set(func(f *fakeAPI) { f.withholdPingToken = true })
		return []resource.TestStep{{
			Config: `
resource "ionhour_project" "p" {
  name = "withheld"
}

resource "ionhour_job" "j" {
  name       = "withheld"
  project_id = ionhour_project.p.id
}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("ionhour_job.j", "ping_token"),
				resource.TestCheckNoResourceAttr("ionhour_job.j", "ping_url"),
			),
		}}
	})
}

func TestAccProviderRejectsBadKey(t *testing.T) {
	runFakeAcc(t, func(t *testing.T, env accEnv) []resource.TestStep {
		t.Setenv(envAPIKey, "ionh_not_the_right_key_0000")
		return []resource.TestStep{{
			Config:      `data "ionhour_regions" "all" {}`,
			ExpectError: regexp.MustCompile(`INVALID_KEY`),
		}}
	})
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// Review fix 2: a batch apply survives a run of 429s (a 60/min plan with
// parallel resources) instead of failing on the second one.
func TestAccRateLimitedBatchApply(t *testing.T) {
	runFakeAcc(t, func(_ *testing.T, env accEnv) []resource.TestStep {
		return []resource.TestStep{{
			PreConfig: func() { env.fake.set(func(f *fakeAPI) { f.rateLimitNext = 6 }) },
			Config: `
resource "ionhour_project" "p" {
  name = "batch"
}

resource "ionhour_check" "c" {
  count      = 3
  name       = "batch ${count.index}"
  project_id = ionhour_project.p.id
  config = {
    url = "https://example.com/${count.index}"
  }
}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("ionhour_check.c.0", "id"),
				resource.TestCheckResourceAttrSet("ionhour_check.c.1", "id"),
				resource.TestCheckResourceAttrSet("ionhour_check.c.2", "id"),
				env.onFake(func(f *fakeAPI) error {
					if n := f.activeChecks(); n != 3 {
						return fmt.Errorf("want 3 checks, got %d", n)
					}
					return nil
				}),
			),
		}}
	})
}
