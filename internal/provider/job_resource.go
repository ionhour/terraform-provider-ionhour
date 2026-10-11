package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/ionhour/go-sdk/api"
)

var (
	_ resource.Resource                = (*jobResource)(nil)
	_ resource.ResourceWithConfigure   = (*jobResource)(nil)
	_ resource.ResourceWithImportState = (*jobResource)(nil)
)

const descPingToken = "Heartbeat credential for `/api/signals/ping/<token>`. Never rotated by updates. " +
	"Null unless the API key is READ_WRITE, projects to MEMBER or higher and covers `mcp:jobs` or " +
	"`mcp:signals`. Sensitive: anyone holding it can send OK heartbeats."

// NewJobResource is the ionhour_job factory.
func NewJobResource() resource.Resource { return &jobResource{} }

type jobResource struct {
	client *apiClient
}

func (r *jobResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_job"
}

func (r *jobResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A heartbeat job: your code pings Ionhour and the job goes LATE, then DOWN, when pings stop.\n\n" +
			"~> `ping_token` and `ping_url` are stored in Terraform state in plain text. Use an encrypted " +
			"remote backend. Reading them back needs a READ_WRITE key with `mcp:jobs` (or `mcp:signals`) " +
			"and MEMBER role or higher; otherwise they are null.\n\n" +
			"Changing `project_id` moves the job in place and keeps its id and ping token. " +
			"Import by numeric id or `slug:<slug>`.\n\n" +
			"~> **Importing an auto-provisioned job.** Jobs created by slug-mode SDK pings carry a slug that " +
			"those pings depend on. Set the same `slug` in configuration before importing: otherwise the next " +
			"apply clears it and slug-mode pings (`/signals/s/<slug>`) stop resolving.\n\n" +
			"Legacy jobs created from an unparseable schedule have no interval. They import with " +
			"`interval_seconds = null`, and the next apply writes `3600` (or the configured value).",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Numeric job id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Job name (1-255 characters).",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Unique per workspace (`^[a-z0-9_-]{1,64}$`). Lets SDKs ping by slug and " +
					"addresses the job as `slug:<slug>`. Removing it clears the slug; deleting the job releases it.",
				Optional:   true,
				Validators: slugValidators(),
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: descProjectID,
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"interval_seconds": schema.Int64Attribute{
				MarkdownDescription: "Expected ping interval in seconds (10-604800). Defaults to `3600`.",
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(defaultJobIntervalSeconds),
				Validators:          []validator.Int64{int64validator.Between(10, 604800)},
			},
			"grace_seconds": schema.Int64Attribute{
				MarkdownDescription: "Lateness tolerated before the job goes LATE, in seconds (5-86400). Defaults to `300`.",
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(defaultJobGraceSeconds),
				Validators:          []validator.Int64{int64validator.Between(5, 86400)},
			},
			"mute_notifications": schema.BoolAttribute{
				MarkdownDescription: "Suppress notifications. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"down_severity": schema.StringAttribute{
				MarkdownDescription: descDownSeverity + " Defaults to `P1`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultDownSeverity),
				Validators:          severityValidators(),
			},
			"late_severity": schema.StringAttribute{
				MarkdownDescription: descLateSeverity + " Defaults to `P3`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultLateSeverity),
				Validators:          severityValidators(),
			},
			"status": schema.StringAttribute{
				MarkdownDescription: descStatus,
				Computed:            true,
			},
			"paused": schema.BoolAttribute{
				MarkdownDescription: descPaused,
				Computed:            true,
			},
			"ping_token": schema.StringAttribute{
				MarkdownDescription: descPingToken,
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseNonNullStateForUnknown()},
			},
			"ping_url": schema.StringAttribute{
				MarkdownDescription: "Full heartbeat URL for this job. Null whenever `ping_token` is null. Sensitive.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseNonNullStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: descCreatedAt,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: descUpdatedAt,
				Computed:            true,
			},
		},
	}
}

func (r *jobResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *jobResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var cfg jobModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.createJob(ctx, jobCreateBody(cfg))
	if err != nil {
		addAPIError(&resp.Diagnostics, "create Ionhour job", err)
		return
	}
	state := jobFromAPI(*out, nil)
	warnWithheldPingToken(&resp.Diagnostics, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *jobResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior jobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.getJob(ctx, idRef(prior.ID))
	if api.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "read Ionhour job", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, jobFromAPI(*out, &prior))...)
}

func (r *jobResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior jobModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.updateJob(ctx, idRef(prior.ID), jobPatchBody(plan, prior))
	if err != nil {
		addAPIError(&resp.Diagnostics, "update Ionhour job", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, jobFromAPI(*out, &prior))...)
}

func (r *jobResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state jobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.deleteJob(ctx, idRef(state.ID)); err != nil && !api.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "delete Ionhour job", err)
	}
}

// ImportState accepts a numeric id or `slug:<slug>`.
func (r *jobResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByIDOrSlug(ctx, "job", req, resp, func(ctx context.Context, ref string) (int64, error) {
		out, err := r.client.getJob(ctx, ref)
		if err != nil {
			return 0, err
		}
		return int64(out.Id), nil
	})
}

func warnWithheldPingToken(diags *diag.Diagnostics, m jobModel) {
	if m.PingToken.IsNull() {
		diags.AddWarning("Ionhour job ping token withheld",
			"The API returned no ping_token for job "+m.Name.ValueString()+". The API key needs READ_WRITE "+
				"access, MEMBER role or higher and the mcp:jobs (or mcp:signals) resource to read it.")
	}
}
