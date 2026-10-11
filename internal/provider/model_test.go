package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

// wire marshals a DTO and decodes it back into a generic map: exactly the
// JSON object the API receives.
func wire(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func assertWire(t *testing.T, v any, want string) {
	t.Helper()
	var w map[string]any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation %s: %v", want, err)
	}
	if got := wire(t, v); !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Errorf("wire body\n got: %s\nwant: %s", gb, want)
	}
}

var (
	created = time.Date(2026, 10, 1, 12, 0, 0, 123e6, time.UTC)
	updated = time.Date(2026, 10, 2, 8, 30, 0, 0, time.FixedZone("CEST", 2*3600))
)

// ---- projects ----

func TestProjectCreateBody(t *testing.T) {
	assertWire(t, projectCreateBody(projectModel{Name: types.StringValue("Payments"), Description: types.StringNull()}),
		`{"name":"Payments"}`)
	assertWire(t, projectCreateBody(projectModel{Name: types.StringValue("Payments"), Description: types.StringValue("d")}),
		`{"name":"Payments","description":"d"}`)
}

func TestProjectPatchBody(t *testing.T) {
	state := projectModel{Name: types.StringValue("A"), Description: types.StringValue("d")}
	assertWire(t, projectPatchBody(state, state), `{}`)
	assertWire(t, projectPatchBody(projectModel{Name: types.StringValue("B"), Description: types.StringNull()}, state),
		`{"name":"B","description":null}`)
	assertWire(t, projectPatchBody(projectModel{Name: types.StringValue("A"), Description: types.StringValue("e")}, state),
		`{"description":"e"}`)
}

func TestProjectFromAPI(t *testing.T) {
	got := projectFromAPI(api.ProjectV1Response{
		Id: 7, Name: "Payments", Description: nullable.NewNullNullable[string](), CreatedAt: created, UpdatedAt: updated,
	})
	want := projectModel{
		ID: types.Int64Value(7), Name: types.StringValue("Payments"), Description: types.StringNull(),
		CreatedAt: types.StringValue("2026-10-01T12:00:00Z"), UpdatedAt: types.StringValue("2026-10-02T06:30:00Z"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("projectFromAPI = %+v, want %+v", got, want)
	}
}

// ---- checks ----

func sampleCheckResponse() api.CheckV1Response {
	return api.CheckV1Response{
		Id: 123, Slug: nullable.NewNullableWithValue("public-api"), Name: "Public API", ProjectId: 7,
		CheckType: api.CheckTypeHTTP, IntervalSeconds: 300, GraceSeconds: 5, DownSeverity: "P1", LateSeverity: "P3",
		Status: "OK", CreatedAt: created, UpdatedAt: updated,
		Config: nullable.NewNullableWithValue(api.V1HttpCheckConfigResponse{
			Url:                            nullable.NewNullableWithValue("https://api.example.com/health"),
			Method:                         nullable.NewNullableWithValue(api.OutboundHttpMethodGET),
			CustomHeaders:                  nullable.NewNullableWithValue(map[string]string{"x-api-key": "s"}),
			TimeoutMs:                      8000,
			ExpectedStatusMin:              nullable.NewNullableWithValue(200),
			ExpectedStatusMax:              nullable.NewNullableWithValue(399),
			Regions:                        []string{"us-east-1", "eu-central-1"},
			FollowRedirects:                nullable.NewNullableWithValue(true),
			MaxRedirects:                   nullable.NewNullableWithValue(3),
			BlockHttpDowngrade:             nullable.NewNullableWithValue(true),
			FailAfterConsecutive:           3,
			ResolveAfterConsecutiveSuccess: 2,
			LatencyWarnMs:                  nullable.NewNullNullable[int](),
			SslExpiryWarnDays:              14,
			BodyContains:                   nullable.NewNullNullable[string](),
			BodyNotContains:                nullable.NewNullableWithValue("error"),
			IpFamily:                       api.IpFamilyAUTO,
			MaxResponseBytes:               262144,
		}),
	}
}

func mustCheckModel(t *testing.T, r api.CheckV1Response, prior *checkModel) checkModel {
	t.Helper()
	m, diags := checkFromAPI(context.Background(), r, prior)
	if diags.HasError() {
		t.Fatalf("checkFromAPI: %v", diags)
	}
	return m
}

func TestCheckFromAPI(t *testing.T) {
	m := mustCheckModel(t, sampleCheckResponse(), nil)
	if m.ID.ValueInt64() != 123 || m.Slug.ValueString() != "public-api" || m.CheckType.ValueString() != "HTTP" {
		t.Errorf("top-level fields wrong: %+v", m)
	}
	if m.CreatedAt.ValueString() != "2026-10-01T12:00:00Z" {
		t.Errorf("created_at = %s", m.CreatedAt)
	}
	c := m.Config
	if c == nil {
		t.Fatal("config is nil")
	}
	if !c.LatencyWarnMs.IsNull() || !c.BodyContains.IsNull() || c.BodyNotContains.ValueString() != "error" {
		t.Errorf("nullable config fields wrong: latency=%v body=%v bodyNot=%v", c.LatencyWarnMs, c.BodyContains, c.BodyNotContains)
	}
	if c.Method.ValueString() != "GET" || c.ExpectedStatusMax.ValueInt64() != 399 || c.MaxResponseBytes.ValueInt64() != 262144 {
		t.Errorf("defaulted config fields wrong: %+v", c)
	}
	var regions []string
	c.Regions.ElementsAs(context.Background(), &regions, false)
	if len(regions) != 2 {
		t.Errorf("regions = %v", regions)
	}
	headers, _ := stringMap(context.Background(), c.CustomHeaders)
	if !reflect.DeepEqual(headers, map[string]string{"x-api-key": "s"}) {
		t.Errorf("headers = %v", headers)
	}
}

func TestCheckFromAPIKeepsConfiguredHeaderCase(t *testing.T) {
	ctx := context.Background()
	prior := mustCheckModel(t, sampleCheckResponse(), nil)
	mixed, _ := mapValue(ctx, map[string]string{"X-Api-Key": "s"})
	prior.Config.CustomHeaders = mixed

	m := mustCheckModel(t, sampleCheckResponse(), &prior)
	if !m.Config.CustomHeaders.Equal(mixed) {
		t.Errorf("custom_headers = %v, want the configured spelling %v", m.Config.CustomHeaders, mixed)
	}
}

func TestCheckFromAPINullConfigAndHeaders(t *testing.T) {
	r := sampleCheckResponse()
	r.Slug = nullable.NewNullNullable[string]()
	r.Config = nullable.NewNullNullable[api.V1HttpCheckConfigResponse]()
	m := mustCheckModel(t, r, nil)
	if m.Config != nil || !m.Slug.IsNull() {
		t.Errorf("want null config and slug, got config=%v slug=%v", m.Config, m.Slug)
	}

	r = sampleCheckResponse()
	cfg := r.Config.MustGet()
	cfg.CustomHeaders = nullable.NewNullNullable[map[string]string]()
	r.Config = nullable.NewNullableWithValue(cfg)
	if m := mustCheckModel(t, r, nil); !m.Config.CustomHeaders.IsNull() {
		t.Errorf("custom_headers = %v, want null", m.Config.CustomHeaders)
	}
}

// configOnly is a check configuration with just the required attributes; the
// rest are null exactly as Terraform passes an unset attribute in Config.
func configOnly(url string) checkModel {
	c := nullCheckConfig()
	c.URL = types.StringValue(url)
	return checkModel{
		Name: types.StringValue("Public API"), ProjectID: types.Int64Value(7), Slug: types.StringNull(),
		CheckType: types.StringNull(), IntervalSeconds: types.Int64Null(), GraceSeconds: types.Int64Null(),
		MuteNotifications: types.BoolNull(), DownSeverity: types.StringNull(), LateSeverity: types.StringNull(),
		Config: &c,
	}
}

func TestCheckCreateBodySendsOnlyConfigured(t *testing.T) {
	body, diags := checkCreateBody(context.Background(), configOnly("https://api.example.com/health"))
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertWire(t, body, `{"name":"Public API","projectId":7,"config":{"url":"https://api.example.com/health"}}`)
}

func TestCheckCreateBodyFull(t *testing.T) {
	ctx := context.Background()
	cfg := configOnly("https://api.example.com/health")
	cfg.Slug = types.StringValue("public-api")
	cfg.CheckType = types.StringValue("HTTP")
	cfg.IntervalSeconds = types.Int64Value(60)
	cfg.LateSeverity = types.StringValue("P2")
	cfg.Config.Method = types.StringValue("HEAD")
	cfg.Config.CustomHeaders, _ = mapValue(ctx, map[string]string{"X-Api-Key": "s", "Accept": "json"})
	cfg.Config.Regions, _ = setValue(ctx, []string{"us-east-1", "eu-central-1"})
	cfg.Config.FollowRedirects = types.BoolValue(false)
	cfg.Config.LatencyWarnMs = types.Int64Value(1500)
	cfg.Config.BodyContains = types.StringValue("ok")

	body, diags := checkCreateBody(ctx, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertWire(t, body, `{
		"name":"Public API","projectId":7,"slug":"public-api","checkType":"HTTP","intervalSeconds":60,"lateSeverity":"P2",
		"config":{"url":"https://api.example.com/health","method":"HEAD",
			"customHeaders":{"x-api-key":"s","accept":"json"},"regions":["eu-central-1","us-east-1"],
			"followRedirects":false,"latencyWarnMs":1500,"bodyContains":"ok"}}`)
}

func TestCheckPatchBodyNoChange(t *testing.T) {
	state := mustCheckModel(t, sampleCheckResponse(), nil)
	body, diags := checkPatchBody(context.Background(), state, state, state)
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertWire(t, body, `{}`)
}

func TestCheckPatchBodyClearsAndResets(t *testing.T) {
	ctx := context.Background()
	state := mustCheckModel(t, sampleCheckResponse(), nil)
	state.Config.TimeoutMs = types.Int64Value(5000)
	state.Config.LatencyWarnMs = types.Int64Value(900)

	// The user removed slug, custom_headers, latency_warn_ms, body_not_contains
	// and timeout_ms from configuration. Plan: defaults for defaulted ones,
	// null for nullable ones, prior state for regions (UseNonNullStateForUnknown).
	plan := mustCheckModel(t, sampleCheckResponse(), nil)
	plan.Slug = types.StringNull()
	plan.Config.CustomHeaders = types.MapNull(types.StringType)
	plan.Config.LatencyWarnMs = types.Int64Null()
	plan.Config.BodyNotContains = types.StringNull()
	plan.Config.TimeoutMs = types.Int64Value(defaultTimeoutMs)

	cfg := configOnly("https://api.example.com/health")

	body, diags := checkPatchBody(ctx, plan, state, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertWire(t, body, `{"slug":null,"config":{
		"customHeaders":null,"latencyWarnMs":null,"bodyNotContains":null,"timeoutMs":null}}`)
}

func TestCheckPatchBodyTopLevelRestatesDefaults(t *testing.T) {
	state := mustCheckModel(t, sampleCheckResponse(), nil)
	state.IntervalSeconds = types.Int64Value(600)
	state.MuteNotifications = types.BoolValue(true)
	plan := mustCheckModel(t, sampleCheckResponse(), nil) // defaults: 300, false
	plan.ProjectID = types.Int64Value(8)
	plan.Name = types.StringValue("Renamed")

	body, diags := checkPatchBody(context.Background(), plan, state, configOnly("https://api.example.com/health"))
	if diags.HasError() {
		t.Fatal(diags)
	}
	assertWire(t, body, `{"name":"Renamed","projectId":8,"intervalSeconds":300,"muteNotifications":false}`)
}

func TestCheckPatchBodyHeaders(t *testing.T) {
	ctx := context.Background()
	state := mustCheckModel(t, sampleCheckResponse(), nil) // {"x-api-key":"s"}

	// Case-only rename: nothing to send.
	plan := mustCheckModel(t, sampleCheckResponse(), nil)
	plan.Config.CustomHeaders, _ = mapValue(ctx, map[string]string{"X-API-KEY": "s"})
	body, _ := checkPatchBody(ctx, plan, state, plan)
	assertWire(t, body, `{}`)

	// Value change: the whole map is sent, lowercased.
	plan.Config.CustomHeaders, _ = mapValue(ctx, map[string]string{"X-API-KEY": "t", "Accept": "json"})
	body, _ = checkPatchBody(ctx, plan, state, plan)
	assertWire(t, body, `{"config":{"customHeaders":{"x-api-key":"t","accept":"json"}}}`)
}

func TestCheckPatchBodyRegions(t *testing.T) {
	ctx := context.Background()
	state := mustCheckModel(t, sampleCheckResponse(), nil)

	plan := mustCheckModel(t, sampleCheckResponse(), nil)
	plan.Config.Regions = types.SetUnknown(types.StringType)
	body, _ := checkPatchBody(ctx, plan, state, plan)
	assertWire(t, body, `{}`)

	plan.Config.Regions, _ = setValue(ctx, []string{"eu-central-1"})
	body, _ = checkPatchBody(ctx, plan, state, plan)
	assertWire(t, body, `{"config":{"regions":["eu-central-1"]}}`)
}

func TestCheckPatchBodyNeverSendsURL(t *testing.T) {
	state := mustCheckModel(t, sampleCheckResponse(), nil)
	plan := mustCheckModel(t, sampleCheckResponse(), nil)
	plan.Config.Method = types.StringValue("POST")
	body, _ := checkPatchBody(context.Background(), plan, state, plan)
	if body.Config == nil || body.Config.Url != nil {
		t.Fatalf("config.url must never be sent on PATCH: %+v", body.Config)
	}
	assertWire(t, body, `{"config":{"method":"POST"}}`)
}

func TestValidateHTTPConfig(t *testing.T) {
	ctx := context.Background()
	ok := nullCheckConfig()
	ok.URL = types.StringValue("https://x")
	if d := validateHTTPConfig(ctx, ok); d.HasError() {
		t.Fatalf("valid config rejected: %v", d)
	}

	bad := ok
	bad.ExpectedStatusMin = types.Int64Value(500)
	bad.ExpectedStatusMax = types.Int64Value(299)
	if d := validateHTTPConfig(ctx, bad); !d.HasError() {
		t.Error("min > max must fail")
	}

	bad = ok
	bad.ExpectedStatusMin = types.Int64Value(404)
	if d := validateHTTPConfig(ctx, bad); !d.HasError() {
		t.Error("min above the default max must fail")
	}

	bad = ok
	bad.CustomHeaders, _ = mapValue(ctx, map[string]string{"X-A": "1", "x-a": "2"})
	if d := validateHTTPConfig(ctx, bad); !d.HasError() {
		t.Error("colliding header names must fail")
	}

	for _, name := range []string{"Authorization", "host", "X-Forwarded-For", "proxy-anything", "__proto__"} {
		bad = ok
		bad.CustomHeaders, _ = mapValue(ctx, map[string]string{name: "1"})
		if d := validateHTTPConfig(ctx, bad); !d.HasError() {
			t.Errorf("reserved header %q must fail", name)
		}
	}

	// Review fix 1: header values unknown at plan time (random_password,
	// another resource's output) must not fail validation; names still do.
	unknownValues := ok
	unknownValues.CustomHeaders = types.MapValueMust(types.StringType, map[string]attr.Value{
		"X-Api-Key": types.StringUnknown(),
		"Accept":    types.StringValue("json"),
	})
	if d := validateHTTPConfig(ctx, unknownValues); d.HasError() {
		t.Errorf("unknown header values must validate: %v", d)
	}
	reservedUnknown := ok
	reservedUnknown.CustomHeaders = types.MapValueMust(types.StringType, map[string]attr.Value{
		"Authorization": types.StringUnknown(),
	})
	if d := validateHTTPConfig(ctx, reservedUnknown); !d.HasError() {
		t.Error("a reserved header name must fail even when its value is unknown")
	}
	collideUnknown := ok
	collideUnknown.CustomHeaders = types.MapValueMust(types.StringType, map[string]attr.Value{
		"X-A": types.StringUnknown(), "x-a": types.StringValue("2"),
	})
	if d := validateHTTPConfig(ctx, collideUnknown); !d.HasError() {
		t.Error("colliding names must fail even when a value is unknown")
	}
	unknownMap := ok
	unknownMap.CustomHeaders = types.MapUnknown(types.StringType)
	if d := validateHTTPConfig(ctx, unknownMap); d.HasError() {
		t.Errorf("an unknown header map must validate: %v", d)
	}

	// Review fix 3: an unknown expected_status_max is not the default.
	unknownMax := ok
	unknownMax.ExpectedStatusMin = types.Int64Value(404)
	unknownMax.ExpectedStatusMax = types.Int64Unknown()
	if d := validateHTTPConfig(ctx, unknownMax); d.HasError() {
		t.Errorf("min 404 with an unknown max must validate: %v", d)
	}
}

// ---- jobs ----

func sampleJobResponse() api.JobV1Response {
	return api.JobV1Response{
		Id: 456, Slug: nullable.NewNullableWithValue("nightly-backup"), Name: "Nightly Backup",
		ProjectId: nullable.NewNullableWithValue(7), IntervalSeconds: nullable.NewNullableWithValue(86400),
		GraceSeconds: 600, DownSeverity: "P1", LateSeverity: "P3", Status: "OK",
		PingToken: nullable.NewNullableWithValue("tok"), PingUrl: nullable.NewNullableWithValue("https://signal/ping/tok"),
		CreatedAt: created, UpdatedAt: updated,
	}
}

func TestJobCreateBody(t *testing.T) {
	cfg := jobModel{
		Name: types.StringValue("Nightly"), ProjectID: types.Int64Value(7), Slug: types.StringNull(),
		IntervalSeconds: types.Int64Null(), GraceSeconds: types.Int64Value(600), MuteNotifications: types.BoolNull(),
		DownSeverity: types.StringNull(), LateSeverity: types.StringNull(),
	}
	assertWire(t, jobCreateBody(cfg), `{"name":"Nightly","projectId":7,"graceSeconds":600}`)
	cfg.Slug = types.StringValue("nightly")
	assertWire(t, jobCreateBody(cfg), `{"name":"Nightly","projectId":7,"graceSeconds":600,"slug":"nightly"}`)
}

func TestJobPatchBody(t *testing.T) {
	state := jobFromAPI(sampleJobResponse(), nil)
	assertWire(t, jobPatchBody(state, state), `{}`)

	plan := state
	plan.Slug = types.StringNull()
	plan.ProjectID = types.Int64Value(8)
	plan.IntervalSeconds = types.Int64Value(defaultJobIntervalSeconds)
	assertWire(t, jobPatchBody(plan, state), `{"slug":null,"projectId":8,"intervalSeconds":3600}`)
}

func TestJobFromAPI(t *testing.T) {
	m := jobFromAPI(sampleJobResponse(), nil)
	if m.PingToken.ValueString() != "tok" || m.IntervalSeconds.ValueInt64() != 86400 || m.ProjectID.ValueInt64() != 7 {
		t.Errorf("jobFromAPI = %+v", m)
	}

	legacy := sampleJobResponse()
	legacy.IntervalSeconds = nullable.NewNullNullable[int]()
	legacy.Slug = nullable.NewNullNullable[string]()
	if m := jobFromAPI(legacy, nil); !m.IntervalSeconds.IsNull() || !m.Slug.IsNull() {
		t.Errorf("legacy job: interval=%v slug=%v, want null", m.IntervalSeconds, m.Slug)
	}
}

func TestJobFromAPIKeepsWithheldPingToken(t *testing.T) {
	prior := jobFromAPI(sampleJobResponse(), nil)
	withheld := sampleJobResponse()
	withheld.PingToken = nullable.NewNullNullable[string]()
	withheld.PingUrl = nullable.NewNullNullable[string]()

	if m := jobFromAPI(withheld, &prior); m.PingToken.ValueString() != "tok" || m.PingURL.ValueString() != "https://signal/ping/tok" {
		t.Errorf("withheld token with known prior: got %v / %v", m.PingToken, m.PingURL)
	}
	if m := jobFromAPI(withheld, nil); !m.PingToken.IsNull() || !m.PingURL.IsNull() {
		t.Errorf("withheld token without prior must be null, got %v", m.PingToken)
	}
}

// ---- regions ----

func TestRegionsFromAPI(t *testing.T) {
	m, diags := regionsFromAPI(context.Background(), []api.RegionV1Response{
		{Id: "us-east-1", DisplayName: "US East", Flag: "us", Enabled: true},
		{Id: "ap-1", DisplayName: "AP", Flag: "sg", Enabled: false},
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var ids []string
	m.IDs.ElementsAs(context.Background(), &ids, false)
	if !reflect.DeepEqual(ids, []string{"us-east-1"}) || len(m.Regions.Elements()) != 2 {
		t.Errorf("ids=%v regions=%d", ids, len(m.Regions.Elements()))
	}
}
