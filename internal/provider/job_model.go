package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ionhour/go-sdk/api"
)

// Contract defaults for jobs.
const (
	defaultJobIntervalSeconds = 3600
	defaultJobGraceSeconds    = 300
)

// jobModel is the state of ionhour_job and the ionhour_job data source.
type jobModel struct {
	ID                types.Int64  `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Slug              types.String `tfsdk:"slug"`
	ProjectID         types.Int64  `tfsdk:"project_id"`
	IntervalSeconds   types.Int64  `tfsdk:"interval_seconds"`
	GraceSeconds      types.Int64  `tfsdk:"grace_seconds"`
	MuteNotifications types.Bool   `tfsdk:"mute_notifications"`
	DownSeverity      types.String `tfsdk:"down_severity"`
	LateSeverity      types.String `tfsdk:"late_severity"`
	Status            types.String `tfsdk:"status"`
	Paused            types.Bool   `tfsdk:"paused"`
	PingToken         types.String `tfsdk:"ping_token"`
	PingURL           types.String `tfsdk:"ping_url"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
}

// jobCreateBody sends only configured attributes; the server applies its
// defaults (which equal the schema defaults) to the rest.
func jobCreateBody(cfg jobModel) api.CreateJobV1Dto {
	return api.CreateJobV1Dto{
		Name:              cfg.Name.ValueString(),
		ProjectId:         int(cfg.ProjectID.ValueInt64()),
		Slug:              createNullable(cfg.Slug, cfg.Slug.ValueString),
		IntervalSeconds:   intPtr(cfg.IntervalSeconds),
		GraceSeconds:      intPtr(cfg.GraceSeconds),
		MuteNotifications: boolPtr(cfg.MuteNotifications),
		DownSeverity:      severityPtr(cfg.DownSeverity),
		LateSeverity:      severityPtr(cfg.LateSeverity),
	}
}

// jobPatchBody sends only what changed. slug is the only nullable key; the
// rest are restated with their (default) planned value when removed.
func jobPatchBody(plan, state jobModel) api.UpdateJobV1Dto {
	var body api.UpdateJobV1Dto
	if !plan.Name.Equal(state.Name) {
		body.Name = stringPtr(plan.Name)
	}
	if s, ok := patchNullable(plan.Slug, state.Slug, plan.Slug, plan.Slug.ValueString); ok {
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
	return body
}

// jobFromAPI maps the API representation to state. The ping token never
// rotates through the API, so when the server withholds it (null: the key
// lacks the ping-token permission) a known prior value is kept instead of
// being erased from state.
func jobFromAPI(j api.JobV1Response, prior *jobModel) jobModel {
	m := jobModel{
		ID:                types.Int64Value(int64(j.Id)),
		Name:              types.StringValue(j.Name),
		Slug:              nullableString(j.Slug),
		ProjectID:         nullableInt64(j.ProjectId),
		IntervalSeconds:   nullableInt64(j.IntervalSeconds),
		GraceSeconds:      types.Int64Value(int64(j.GraceSeconds)),
		MuteNotifications: types.BoolValue(j.MuteNotifications),
		DownSeverity:      types.StringValue(string(j.DownSeverity)),
		LateSeverity:      types.StringValue(string(j.LateSeverity)),
		Status:            types.StringValue(string(j.Status)),
		Paused:            types.BoolValue(j.Paused),
		PingToken:         nullableString(j.PingToken),
		PingURL:           nullableString(j.PingUrl),
		CreatedAt:         timestamp(j.CreatedAt),
		UpdatedAt:         timestamp(j.UpdatedAt),
	}
	if prior != nil && m.PingToken.IsNull() && known(prior.PingToken) {
		m.PingToken = prior.PingToken
		m.PingURL = prior.PingURL
	}
	return m
}
