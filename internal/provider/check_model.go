package provider

import (
	"context"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

// Contract defaults for HTTP checks (contracts/v1-resources.md). They are the
// schema defaults, so an omitted attribute plans exactly what the server
// stores and never produces a perpetual diff.
const (
	defaultCheckType                 = "HTTP"
	defaultCheckIntervalSeconds      = 300
	defaultCheckGraceSeconds         = 5
	defaultDownSeverity              = "P1"
	defaultLateSeverity              = "P3"
	defaultMethod                    = "GET"
	defaultTimeoutMs                 = 8000
	defaultExpectedStatusMin         = 200
	defaultExpectedStatusMax         = 399
	defaultFollowRedirects           = true
	defaultMaxRedirects              = 3
	defaultBlockHTTPDowngrade        = true
	defaultFailAfterConsecutive      = 3
	defaultResolveAfterConsecutiveOK = 2
	defaultSSLExpiryWarnDays         = 14
)

// checkModel is the state of ionhour_check and the ionhour_check data source.
type checkModel struct {
	ID                types.Int64       `tfsdk:"id"`
	Name              types.String      `tfsdk:"name"`
	Slug              types.String      `tfsdk:"slug"`
	ProjectID         types.Int64       `tfsdk:"project_id"`
	CheckType         types.String      `tfsdk:"check_type"`
	IntervalSeconds   types.Int64       `tfsdk:"interval_seconds"`
	GraceSeconds      types.Int64       `tfsdk:"grace_seconds"`
	MuteNotifications types.Bool        `tfsdk:"mute_notifications"`
	DownSeverity      types.String      `tfsdk:"down_severity"`
	LateSeverity      types.String      `tfsdk:"late_severity"`
	Config            *checkConfigModel `tfsdk:"config"`
	Status            types.String      `tfsdk:"status"`
	Paused            types.Bool        `tfsdk:"paused"`
	CreatedAt         types.String      `tfsdk:"created_at"`
	UpdatedAt         types.String      `tfsdk:"updated_at"`
}

// checkConfigModel is the HTTP `config` nested attribute.
type checkConfigModel struct {
	URL                            types.String `tfsdk:"url"`
	Method                         types.String `tfsdk:"method"`
	CustomHeaders                  types.Map    `tfsdk:"custom_headers"`
	TimeoutMs                      types.Int64  `tfsdk:"timeout_ms"`
	ExpectedStatusMin              types.Int64  `tfsdk:"expected_status_min"`
	ExpectedStatusMax              types.Int64  `tfsdk:"expected_status_max"`
	Regions                        types.Set    `tfsdk:"regions"`
	FollowRedirects                types.Bool   `tfsdk:"follow_redirects"`
	MaxRedirects                   types.Int64  `tfsdk:"max_redirects"`
	BlockHTTPDowngrade             types.Bool   `tfsdk:"block_http_downgrade"`
	FailAfterConsecutive           types.Int64  `tfsdk:"fail_after_consecutive"`
	ResolveAfterConsecutiveSuccess types.Int64  `tfsdk:"resolve_after_consecutive_success"`
	LatencyWarnMs                  types.Int64  `tfsdk:"latency_warn_ms"`
	SSLExpiryWarnDays              types.Int64  `tfsdk:"ssl_expiry_warn_days"`
	BodyContains                   types.String `tfsdk:"body_contains"`
	BodyNotContains                types.String `tfsdk:"body_not_contains"`
	IPFamily                       types.String `tfsdk:"ip_family"`
	SkipTLSVerification            types.Bool   `tfsdk:"skip_tls_verification"`
	MaxResponseBytes               types.Int64  `tfsdk:"max_response_bytes"`
}

// nullCheckConfig is a config whose every attribute is a typed null; it
// stands in for an absent config so comparisons stay type-correct.
func nullCheckConfig() checkConfigModel {
	return checkConfigModel{
		URL:                            types.StringNull(),
		Method:                         types.StringNull(),
		CustomHeaders:                  types.MapNull(types.StringType),
		TimeoutMs:                      types.Int64Null(),
		ExpectedStatusMin:              types.Int64Null(),
		ExpectedStatusMax:              types.Int64Null(),
		Regions:                        types.SetNull(types.StringType),
		FollowRedirects:                types.BoolNull(),
		MaxRedirects:                   types.Int64Null(),
		BlockHTTPDowngrade:             types.BoolNull(),
		FailAfterConsecutive:           types.Int64Null(),
		ResolveAfterConsecutiveSuccess: types.Int64Null(),
		LatencyWarnMs:                  types.Int64Null(),
		SSLExpiryWarnDays:              types.Int64Null(),
		BodyContains:                   types.StringNull(),
		BodyNotContains:                types.StringNull(),
		IPFamily:                       types.StringNull(),
		SkipTLSVerification:            types.BoolNull(),
		MaxResponseBytes:               types.Int64Null(),
	}
}

func configOrNull(c *checkConfigModel) checkConfigModel {
	if c == nil {
		return nullCheckConfig()
	}
	return *c
}

func int64Value(v types.Int64) func() int   { return func() int { return int(v.ValueInt64()) } }
func boolValue(v types.Bool) func() bool    { return v.ValueBool }
func strValue(v types.String) func() string { return v.ValueString }
func methodValue(v types.String) func() api.OutboundHttpMethod {
	return func() api.OutboundHttpMethod { return api.OutboundHttpMethod(v.ValueString()) }
}

// checkCreateBody builds POST /v1/checks from the *configuration* (not the
// plan): only attributes the user set are sent and the server applies its
// own defaults to the rest. That keeps plan-gated defaults such as
// ssl_expiry_warn_days from counting as "set" on create.
func checkCreateBody(ctx context.Context, cfg checkModel) (api.CreateCheckV1Dto, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := api.CreateCheckV1Dto{
		Name:              cfg.Name.ValueString(),
		ProjectId:         int(cfg.ProjectID.ValueInt64()),
		Slug:              createNullable(cfg.Slug, cfg.Slug.ValueString),
		IntervalSeconds:   intPtr(cfg.IntervalSeconds),
		GraceSeconds:      intPtr(cfg.GraceSeconds),
		MuteNotifications: boolPtr(cfg.MuteNotifications),
		DownSeverity:      severityPtr(cfg.DownSeverity),
		LateSeverity:      severityPtr(cfg.LateSeverity),
	}
	if known(cfg.CheckType) {
		t := api.CheckType(cfg.CheckType.ValueString())
		body.CheckType = &t
	}
	c := configOrNull(cfg.Config)
	headers, d := stringMap(ctx, c.CustomHeaders)
	diags.Append(d...)
	regions, d := stringSet(ctx, c.Regions)
	diags.Append(d...)

	body.Config = api.V1HttpCheckConfigDto{
		Url:                            c.URL.ValueString(),
		Method:                         createNullable(c.Method, methodValue(c.Method)),
		TimeoutMs:                      createNullable(c.TimeoutMs, int64Value(c.TimeoutMs)),
		ExpectedStatusMin:              createNullable(c.ExpectedStatusMin, int64Value(c.ExpectedStatusMin)),
		ExpectedStatusMax:              createNullable(c.ExpectedStatusMax, int64Value(c.ExpectedStatusMax)),
		FollowRedirects:                createNullable(c.FollowRedirects, boolValue(c.FollowRedirects)),
		MaxRedirects:                   createNullable(c.MaxRedirects, int64Value(c.MaxRedirects)),
		BlockHttpDowngrade:             createNullable(c.BlockHTTPDowngrade, boolValue(c.BlockHTTPDowngrade)),
		FailAfterConsecutive:           createNullable(c.FailAfterConsecutive, int64Value(c.FailAfterConsecutive)),
		ResolveAfterConsecutiveSuccess: createNullable(c.ResolveAfterConsecutiveSuccess, int64Value(c.ResolveAfterConsecutiveSuccess)),
		LatencyWarnMs:                  createNullable(c.LatencyWarnMs, int64Value(c.LatencyWarnMs)),
		SslExpiryWarnDays:              createNullable(c.SSLExpiryWarnDays, int64Value(c.SSLExpiryWarnDays)),
		BodyContains:                   createNullable(c.BodyContains, strValue(c.BodyContains)),
		BodyNotContains:                createNullable(c.BodyNotContains, strValue(c.BodyNotContains)),
	}
	if headers != nil {
		body.Config.CustomHeaders = nullable.NewNullableWithValue(lowercaseKeys(headers))
	}
	if regions != nil {
		body.Config.Regions = nullable.NewNullableWithValue(regions)
	}
	return body, diags
}

// checkPatchBody builds PATCH /v1/checks/:id from the difference between the
// plan and the prior state. config.url and check_type are RequiresReplace and
// are never sent. A config attribute removed from configuration is sent as an
// explicit null (the server clears nullable fields and resets defaulted ones
// to the contract default); a removed top-level attribute is sent as its
// schema default because the API rejects null there.
func checkPatchBody(ctx context.Context, plan, state, cfg checkModel) (api.UpdateCheckV1Dto, diag.Diagnostics) {
	var body api.UpdateCheckV1Dto
	if !plan.Name.Equal(state.Name) {
		body.Name = stringPtr(plan.Name)
	}
	if s, ok := patchNullable(plan.Slug, state.Slug, cfg.Slug, plan.Slug.ValueString); ok {
		body.Slug = s
	}
	if !plan.ProjectID.Equal(state.ProjectID) {
		body.ProjectId = intPtr(plan.ProjectID)
	}
	if !plan.IntervalSeconds.Equal(state.IntervalSeconds) {
		body.IntervalSeconds = intPtr(plan.IntervalSeconds)
	}
	if !plan.GraceSeconds.Equal(state.GraceSeconds) {
		body.GraceSeconds = intPtr(plan.GraceSeconds)
	}
	if !plan.MuteNotifications.Equal(state.MuteNotifications) {
		body.MuteNotifications = boolPtr(plan.MuteNotifications)
	}
	if !plan.DownSeverity.Equal(state.DownSeverity) {
		body.DownSeverity = severityPtr(plan.DownSeverity)
	}
	if !plan.LateSeverity.Equal(state.LateSeverity) {
		body.LateSeverity = severityPtr(plan.LateSeverity)
	}

	patch, changed, diags := httpConfigPatch(ctx, configOrNull(plan.Config), configOrNull(state.Config), configOrNull(cfg.Config))
	if changed {
		body.Config = &patch
	}
	return body, diags
}

func httpConfigPatch(ctx context.Context, plan, state, cfg checkConfigModel) (api.V1HttpCheckConfigPatchDto, bool, diag.Diagnostics) {
	var (
		p       api.V1HttpCheckConfigPatchDto
		changed bool
		diags   diag.Diagnostics
	)
	set := func(ok bool) { changed = changed || ok }

	var ok bool
	p.Method, ok = patchNullable(plan.Method, state.Method, cfg.Method, methodValue(plan.Method))
	set(ok)
	p.TimeoutMs, ok = patchNullable(plan.TimeoutMs, state.TimeoutMs, cfg.TimeoutMs, int64Value(plan.TimeoutMs))
	set(ok)
	p.ExpectedStatusMin, ok = patchNullable(plan.ExpectedStatusMin, state.ExpectedStatusMin, cfg.ExpectedStatusMin, int64Value(plan.ExpectedStatusMin))
	set(ok)
	p.ExpectedStatusMax, ok = patchNullable(plan.ExpectedStatusMax, state.ExpectedStatusMax, cfg.ExpectedStatusMax, int64Value(plan.ExpectedStatusMax))
	set(ok)
	p.FollowRedirects, ok = patchNullable(plan.FollowRedirects, state.FollowRedirects, cfg.FollowRedirects, boolValue(plan.FollowRedirects))
	set(ok)
	p.MaxRedirects, ok = patchNullable(plan.MaxRedirects, state.MaxRedirects, cfg.MaxRedirects, int64Value(plan.MaxRedirects))
	set(ok)
	p.BlockHttpDowngrade, ok = patchNullable(plan.BlockHTTPDowngrade, state.BlockHTTPDowngrade, cfg.BlockHTTPDowngrade, boolValue(plan.BlockHTTPDowngrade))
	set(ok)
	p.FailAfterConsecutive, ok = patchNullable(plan.FailAfterConsecutive, state.FailAfterConsecutive, cfg.FailAfterConsecutive, int64Value(plan.FailAfterConsecutive))
	set(ok)
	p.ResolveAfterConsecutiveSuccess, ok = patchNullable(plan.ResolveAfterConsecutiveSuccess, state.ResolveAfterConsecutiveSuccess, cfg.ResolveAfterConsecutiveSuccess, int64Value(plan.ResolveAfterConsecutiveSuccess))
	set(ok)
	p.LatencyWarnMs, ok = patchNullable(plan.LatencyWarnMs, state.LatencyWarnMs, cfg.LatencyWarnMs, int64Value(plan.LatencyWarnMs))
	set(ok)
	p.SslExpiryWarnDays, ok = patchNullable(plan.SSLExpiryWarnDays, state.SSLExpiryWarnDays, cfg.SSLExpiryWarnDays, int64Value(plan.SSLExpiryWarnDays))
	set(ok)
	p.BodyContains, ok = patchNullable(plan.BodyContains, state.BodyContains, cfg.BodyContains, strValue(plan.BodyContains))
	set(ok)
	p.BodyNotContains, ok = patchNullable(plan.BodyNotContains, state.BodyNotContains, cfg.BodyNotContains, strValue(plan.BodyNotContains))
	set(ok)

	headers, ok, d := patchHeaders(ctx, plan.CustomHeaders, state.CustomHeaders)
	diags.Append(d...)
	p.CustomHeaders = headers
	set(ok)

	regions, ok, d := patchRegions(ctx, plan.Regions, state.Regions)
	diags.Append(d...)
	p.Regions = regions
	set(ok)

	return p, changed, diags
}

// patchHeaders compares header maps case-insensitively on their names: a
// case-only rename is not a change (the server stores lowercase anyway).
func patchHeaders(ctx context.Context, plan, state types.Map) (nullable.Nullable[map[string]string], bool, diag.Diagnostics) {
	if plan.IsUnknown() {
		return nullable.Nullable[map[string]string]{}, false, nil
	}
	if plan.IsNull() {
		if state.IsNull() {
			return nullable.Nullable[map[string]string]{}, false, nil
		}
		return nullable.NewNullNullable[map[string]string](), true, nil
	}
	var diags diag.Diagnostics
	planMap, d := stringMap(ctx, plan)
	diags.Append(d...)
	stateMap, d := stringMap(ctx, state)
	diags.Append(d...)
	lowered := lowercaseKeys(planMap)
	if stateMap != nil && maps.Equal(lowered, lowercaseKeys(stateMap)) {
		return nullable.Nullable[map[string]string]{}, false, diags
	}
	return nullable.NewNullableWithValue(lowered), true, diags
}

// patchRegions sends the planned set when it is known and differs. regions is
// Optional+Computed, so an unconfigured value keeps the prior state and is
// never sent.
func patchRegions(ctx context.Context, plan, state types.Set) (nullable.Nullable[[]string], bool, diag.Diagnostics) {
	if plan.IsUnknown() || plan.IsNull() || plan.Equal(state) {
		return nullable.Nullable[[]string]{}, false, nil
	}
	regions, diags := stringSet(ctx, plan)
	return nullable.NewNullableWithValue(regions), true, diags
}

// checkFromAPI maps the API representation to Terraform state. prior is the
// plan (after create/update) or the prior state (on read); it is used only to
// keep the configured spelling of custom header names.
func checkFromAPI(ctx context.Context, r api.CheckV1Response, prior *checkModel) (checkModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	m := checkModel{
		ID:                types.Int64Value(int64(r.Id)),
		Name:              types.StringValue(r.Name),
		Slug:              nullableString(r.Slug),
		ProjectID:         types.Int64Value(int64(r.ProjectId)),
		CheckType:         types.StringValue(string(r.CheckType)),
		IntervalSeconds:   types.Int64Value(int64(r.IntervalSeconds)),
		GraceSeconds:      types.Int64Value(int64(r.GraceSeconds)),
		MuteNotifications: types.BoolValue(r.MuteNotifications),
		DownSeverity:      types.StringValue(string(r.DownSeverity)),
		LateSeverity:      types.StringValue(string(r.LateSeverity)),
		Status:            types.StringValue(string(r.Status)),
		Paused:            types.BoolValue(r.Paused),
		CreatedAt:         timestamp(r.CreatedAt),
		UpdatedAt:         timestamp(r.UpdatedAt),
	}
	if r.Config.IsSpecified() && !r.Config.IsNull() {
		var priorCfg *checkConfigModel
		if prior != nil {
			priorCfg = prior.Config
		}
		cfg, d := httpConfigFromAPI(ctx, r.Config.MustGet(), priorCfg)
		diags.Append(d...)
		m.Config = &cfg
	}
	return m, diags
}

func httpConfigFromAPI(ctx context.Context, c api.V1HttpCheckConfigResponse, prior *checkConfigModel) (checkConfigModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := checkConfigModel{
		URL:                            nullableString(c.Url),
		Method:                         nullableMethod(c.Method),
		TimeoutMs:                      types.Int64Value(int64(c.TimeoutMs)),
		ExpectedStatusMin:              nullableInt64(c.ExpectedStatusMin),
		ExpectedStatusMax:              nullableInt64(c.ExpectedStatusMax),
		FollowRedirects:                nullableBool(c.FollowRedirects),
		MaxRedirects:                   nullableInt64(c.MaxRedirects),
		BlockHTTPDowngrade:             nullableBool(c.BlockHttpDowngrade),
		FailAfterConsecutive:           types.Int64Value(int64(c.FailAfterConsecutive)),
		ResolveAfterConsecutiveSuccess: types.Int64Value(int64(c.ResolveAfterConsecutiveSuccess)),
		LatencyWarnMs:                  nullableInt64(c.LatencyWarnMs),
		SSLExpiryWarnDays:              types.Int64Value(int64(c.SslExpiryWarnDays)),
		BodyContains:                   nullableString(c.BodyContains),
		BodyNotContains:                nullableString(c.BodyNotContains),
		IPFamily:                       types.StringValue(string(c.IpFamily)),
		SkipTLSVerification:            types.BoolValue(c.SkipTlsVerification),
		MaxResponseBytes:               types.Int64Value(int64(c.MaxResponseBytes)),
	}

	var serverHeaders map[string]string
	if c.CustomHeaders.IsSpecified() && !c.CustomHeaders.IsNull() {
		serverHeaders = c.CustomHeaders.MustGet()
		if serverHeaders == nil {
			serverHeaders = map[string]string{}
		}
	}
	var priorHeaders map[string]string
	if prior != nil {
		h, d := stringMap(ctx, prior.CustomHeaders)
		diags.Append(d...)
		priorHeaders = h
	}
	headers, d := mapValue(ctx, reconcileHeaders(serverHeaders, priorHeaders))
	diags.Append(d...)
	out.CustomHeaders = headers

	regions, d := setValue(ctx, c.Regions)
	diags.Append(d...)
	out.Regions = regions
	return out, diags
}
