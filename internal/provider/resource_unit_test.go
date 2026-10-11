package provider

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

func resourceSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return resp.Schema
}

func stateFor(t *testing.T, sch schema.Schema, model any) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	st := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	if diags := st.Set(ctx, model); diags.HasError() {
		t.Fatalf("state.Set: %v", diags)
	}
	return st
}

func emptyState(sch schema.Schema) tfsdk.State {
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}
}

func configured[T interface {
	Configure(context.Context, resource.ConfigureRequest, *resource.ConfigureResponse)
}](t *testing.T, r T, c *apiClient) T {
	t.Helper()
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return r
}

func TestSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "ionhour"}, &meta)
		if diags := resourceSchema(t, r).ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", meta.TypeName, diags)
		}
	}
	for _, newDataSource := range p.DataSources(ctx) {
		d := newDataSource()
		var meta datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "ionhour"}, &meta)
		var resp datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", meta.TypeName, diags)
		}
	}
}

func TestReadRemovesMissingResources(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()

	cases := []struct {
		name  string
		r     resource.Resource
		model any
	}{
		{"project", configured(t, &projectResource{}, c), projectFromAPI(api.ProjectV1Response{Id: 9999, Name: "gone"})},
		{"check", configured(t, &checkResource{}, c), func() any {
			r := sampleCheckResponse()
			r.Id = 9999
			return mustCheckModel(t, r, nil)
		}()},
		{"job", configured(t, &jobResource{}, c), func() any {
			r := sampleJobResponse()
			r.Id = 9999
			return jobFromAPI(r, nil)
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := resourceSchema(t, tc.r)
			st := stateFor(t, sch, tc.model)
			resp := resource.ReadResponse{State: st}
			tc.r.Read(ctx, resource.ReadRequest{State: st}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("Read on a 404 must not error: %v", resp.Diagnostics)
			}
			if !resp.State.Raw.IsNull() {
				t.Fatal("Read on a 404 must remove the resource from state")
			}

			del := resource.DeleteResponse{State: st}
			tc.r.Delete(ctx, resource.DeleteRequest{State: st}, &del)
			if del.Diagnostics.HasError() {
				t.Fatalf("Delete on a 404 must succeed: %v", del.Diagnostics)
			}
		})
	}
}

func TestProjectDeleteNotEmpty(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	pid := mustCreateProject(t, c, "P")
	f.addCheckOutOfBand(pid)

	r := configured(t, &projectResource{}, c)
	st := stateFor(t, resourceSchema(t, r), projectFromAPI(api.ProjectV1Response{Id: pid, Name: "P"}))
	resp := resource.DeleteResponse{State: st}
	r.Delete(ctx, resource.DeleteRequest{State: st}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Summary(), codeProjectNotEmpty) {
		t.Fatalf("want a PROJECT_NOT_EMPTY error, got %v", resp.Diagnostics)
	}
}

func TestImportStateBySlug(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	pid := mustCreateProject(t, c, "P")
	body := sampleJobCreate(pid, "nightly")
	j, err := c.createJob(ctx, body)
	if err != nil {
		t.Fatal(err)
	}

	r := configured(t, &jobResource{}, c)
	sch := resourceSchema(t, r)
	for _, tc := range []struct {
		id      string
		wantID  int64
		wantErr string
	}{
		{id: "slug:nightly", wantID: int64(j.Id)},
		{id: idRefInt(j.Id), wantID: int64(j.Id)},
		{id: "slug:missing", wantErr: "NOT_FOUND"},
		{id: "nightly", wantErr: "Invalid import ID"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: emptyState(sch)}
			r.ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &resp)
			if tc.wantErr != "" {
				if !resp.Diagnostics.HasError() || !strings.Contains(diagText(resp.Diagnostics), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, resp.Diagnostics)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			var id types.Int64
			resp.State.GetAttribute(ctx, path.Root("id"), &id)
			if id.ValueInt64() != tc.wantID {
				t.Fatalf("imported id = %v, want %d", id, tc.wantID)
			}
		})
	}
}

func TestProjectImportRejectsSlug(t *testing.T) {
	r := &projectResource{}
	sch := resourceSchema(t, r)
	resp := resource.ImportStateResponse{State: emptyState(sch)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "slug:payments"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("projects have no slug; slug import must fail")
	}
}

func diagText(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary() + ": " + d.Detail() + "\n")
	}
	return b.String()
}

// ---- provider Configure ----

func configureProvider(t *testing.T, apiKey, baseURL types.String) provider.ConfigureResponse {
	t.Helper()
	return configureProviderCtx(t, context.Background(), apiKey, baseURL)
}

func configureProviderCtx(t *testing.T, ctx context.Context, apiKey, baseURL types.String) provider.ConfigureResponse {
	t.Helper()
	p := New("test")()
	var sresp provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &sresp)
	raw := tftypes.NewValue(sresp.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"api_key":  toTF(apiKey),
		"base_url": toTF(baseURL),
	})
	var resp provider.ConfigureResponse
	p.Configure(ctx, provider.ConfigureRequest{
		Config:           tfsdk.Config{Schema: sresp.Schema, Raw: raw},
		TerraformVersion: "1.13.0",
	}, &resp)
	return resp
}

func toTF(v types.String) tftypes.Value {
	if v.IsNull() {
		return tftypes.NewValue(tftypes.String, nil)
	}
	return tftypes.NewValue(tftypes.String, v.ValueString())
}

func TestProviderConfigure(t *testing.T) {
	f := newFakeAPI(t)
	t.Setenv(envAPIKey, "")
	t.Setenv(envBaseURL, "")

	t.Run("missing key", func(t *testing.T) {
		resp := configureProvider(t, types.StringNull(), types.StringValue(f.baseURL()))
		if !resp.Diagnostics.HasError() || !strings.Contains(diagText(resp.Diagnostics), "Missing Ionhour API key") {
			t.Fatalf("got %v", resp.Diagnostics)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		resp := configureProvider(t, types.StringValue("ionh_wrong_key_000000"), types.StringValue(f.baseURL()))
		text := diagText(resp.Diagnostics)
		if !resp.Diagnostics.HasError() || !strings.Contains(text, "INVALID_KEY") {
			t.Fatalf("got %v", resp.Diagnostics)
		}
		if strings.Contains(text, "ionh_wrong_key_000000") {
			t.Fatal("diagnostic leaked the API key")
		}
	})

	t.Run("invalid base url", func(t *testing.T) {
		resp := configureProvider(t, types.StringValue(f.key), types.StringValue("ftp://nope"))
		if !resp.Diagnostics.HasError() {
			t.Fatal("ftp base_url must be rejected")
		}
	})

	t.Run("ok from attributes logs the workspace without a diagnostic", func(t *testing.T) {
		var logs bytes.Buffer
		ctx := tflogtest.RootLogger(context.Background(), &logs)
		resp := configureProviderCtx(t, ctx, types.StringValue(f.key), types.StringValue(f.baseURL()))
		if len(resp.Diagnostics) != 0 {
			t.Fatalf("a successful configure must raise no diagnostics, got %v", resp.Diagnostics)
		}
		if resp.ResourceData == nil || resp.DataSourceData == nil {
			t.Fatal("provider data not set")
		}
		entries, err := tflogtest.MultilineJSONDecode(&logs)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, e := range entries {
			if e["@message"] == "authenticated to Ionhour workspace" {
				found = e["@level"] == "info" && e["workspace_name"] == "Acceptance" &&
					e["workspace_id"] == float64(42) && e["projected_role"] == "ADMIN" &&
					e["principal_kind"] == "service" && e["permission_level"] == "READ_WRITE"
			}
		}
		if !found {
			t.Fatalf("no INFO log with the workspace; logs: %v", entries)
		}
		if strings.Contains(logs.String(), f.key) {
			t.Fatal("log leaked the API key")
		}
		ua := f.requests("GET", "/api/v1/whoami")[0].Header.Get("User-Agent")
		if ua != "terraform-provider-ionhour/test terraform/1.13.0" {
			t.Errorf("User-Agent = %q", ua)
		}
	})

	t.Run("ok from environment", func(t *testing.T) {
		t.Setenv(envAPIKey, f.key)
		t.Setenv(envBaseURL, f.baseURL())
		resp := configureProvider(t, types.StringNull(), types.StringNull())
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
	})
}

func sampleJobCreate(projectID int, slug string) api.CreateJobV1Dto {
	cfg := jobModel{
		Name: types.StringValue("Nightly"), ProjectID: types.Int64Value(int64(projectID)), Slug: types.StringValue(slug),
		IntervalSeconds: types.Int64Null(), GraceSeconds: types.Int64Null(), MuteNotifications: types.BoolNull(),
		DownSeverity: types.StringNull(), LateSeverity: types.StringNull(),
	}
	return jobCreateBody(cfg)
}

// checkConfigValue builds a raw ionhour_check configuration whose top-level
// attributes are null except name and the given config object.
func checkConfigValue(t *testing.T, sch schema.Schema, config func(tftypes.Object) tftypes.Value) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	typ := sch.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["name"] = tftypes.NewValue(tftypes.String, "x")
	vals["config"] = config(typ.AttributeTypes["config"].(tftypes.Object))
	return tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(typ, vals)}
}

func TestCheckValidateConfig(t *testing.T) {
	r := &checkResource{}
	sch := resourceSchema(t, r)
	ctx := context.Background()

	unknown := checkConfigValue(t, sch, func(typ tftypes.Object) tftypes.Value {
		return tftypes.NewValue(typ, tftypes.UnknownValue)
	})
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: unknown}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an unknown config object must validate: %v", resp.Diagnostics)
	}

	reserved := checkConfigValue(t, sch, func(typ tftypes.Object) tftypes.Value {
		vals := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["url"] = tftypes.NewValue(tftypes.String, "https://x.example.com")
		vals["custom_headers"] = tftypes.NewValue(tftypes.Map{ElementType: tftypes.String},
			map[string]tftypes.Value{"Authorization": tftypes.NewValue(tftypes.String, "Bearer x")})
		return tftypes.NewValue(typ, vals)
	})
	resp = resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: reserved}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(diagText(resp.Diagnostics), "Reserved header names") {
		t.Fatalf("an Authorization header must be rejected at plan time, got %v", resp.Diagnostics)
	}
}

// Review fix 4: a ping token withheld at create (null in state) can become
// visible later; the plan must leave it unknown rather than pin the null.
func TestJobPingTokenPlanKeepsNullStateUnknown(t *testing.T) {
	ctx := context.Background()
	sch := resourceSchema(t, &jobResource{})
	prior := stateFor(t, sch, jobFromAPI(func() api.JobV1Response {
		r := sampleJobResponse()
		r.PingToken = nullable.NewNullNullable[string]()
		r.PingUrl = nullable.NewNullNullable[string]()
		return r
	}(), nil))

	for _, name := range []string{"ping_token", "ping_url"} {
		attrSchema, ok := sch.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute", name)
		}
		req := planmodifier.StringRequest{
			Path:        path.Root(name),
			State:       prior,
			StateValue:  types.StringNull(),
			PlanValue:   types.StringUnknown(),
			ConfigValue: types.StringNull(),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		for _, m := range attrSchema.PlanModifiers {
			m.PlanModifyString(ctx, req, resp)
		}
		if !resp.PlanValue.IsUnknown() {
			t.Errorf("%s: a null prior value must stay unknown in the plan, got %v", name, resp.PlanValue)
		}

		// A known token is still carried forward.
		req.StateValue = types.StringValue("tok")
		resp = &planmodifier.StringResponse{PlanValue: types.StringUnknown()}
		for _, m := range attrSchema.PlanModifiers {
			m.PlanModifyString(ctx, req, resp)
		}
		if resp.PlanValue.ValueString() != "tok" {
			t.Errorf("%s: a known prior value must be kept, got %v", name, resp.PlanValue)
		}
	}
}

// Review fix 6: non-HTTP checks fail clearly instead of planning a replacement.
func TestCheckReadAndImportRejectNonHTTP(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	pid := mustCreateProject(t, c, "P")
	id := f.addCheckOutOfBand(pid)
	f.set(func(f *fakeAPI) {
		f.checks[id].checkType = "TCP"
		slug := "db-port"
		f.checks[id].slug = &slug
	})

	r := configured(t, &checkResource{}, c)
	sch := resourceSchema(t, r)

	st := stateFor(t, sch, func() checkModel {
		m := mustCheckModel(t, sampleCheckResponse(), nil)
		m.ID = types.Int64Value(int64(id))
		return m
	}())
	readResp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &readResp)
	if !strings.Contains(diagText(readResp.Diagnostics), "HTTP checks only in v0.1") {
		t.Fatalf("Read of a TCP check: want the HTTP-only error, got %v", readResp.Diagnostics)
	}

	for _, importID := range []string{"slug:db-port", idRefInt(id)} {
		resp := resource.ImportStateResponse{State: emptyState(sch)}
		r.ImportState(ctx, resource.ImportStateRequest{ID: importID}, &resp)
		if !strings.Contains(diagText(resp.Diagnostics), "HTTP checks only in v0.1") {
			t.Errorf("import %s: want the HTTP-only error, got %v", importID, resp.Diagnostics)
		}
	}

	// The data source still reads it (listing is allowed by the contract).
	d := &checkDataSource{client: c}
	var dsSchema datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &dsSchema)
	cfg := tfsdk.Config{Schema: dsSchema.Schema, Raw: dataSourceConfig(ctx, dsSchema.Schema, map[string]tftypes.Value{
		"slug": tftypes.NewValue(tftypes.String, "db-port"),
	})}
	dsResp := datasource.ReadResponse{State: tfsdk.State{Schema: dsSchema.Schema, Raw: tftypes.NewValue(dsSchema.Schema.Type().TerraformType(ctx), nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: cfg}, &dsResp)
	if dsResp.Diagnostics.HasError() {
		t.Fatalf("the data source must read a TCP check: %v", dsResp.Diagnostics)
	}
}

func dataSourceConfig(ctx context.Context, sch dsschema.Schema, set map[string]tftypes.Value) tftypes.Value {
	typ := sch.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tftypes.NewValue(typ, vals)
}
