package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var (
	_ datasource.DataSource                     = (*jobDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*jobDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*jobDataSource)(nil)
)

// NewJobDataSource is the ionhour_job data source factory.
func NewJobDataSource() datasource.DataSource { return &jobDataSource{} }

type jobDataSource struct {
	client *apiClient
}

func (d *jobDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_job"
}

func (d *jobDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
	}
	boolean := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an Ionhour heartbeat job by `id` or `slug` (exactly one). " +
			"Use it to hand a job's `ping_url` to the system that runs the job.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Numeric job id. Set exactly one of `id` and `slug`.",
				Optional:            true,
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Job slug. Set exactly one of `id` and `slug`.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String(slugValidators()),
			},
			"name":               str("Job name."),
			"project_id":         num(descProjectID),
			"interval_seconds":   num("Expected ping interval in seconds. Null only for legacy jobs created from an unparseable schedule."),
			"grace_seconds":      num("Lateness tolerated before the job goes LATE, in seconds."),
			"mute_notifications": boolean("Whether notifications are suppressed."),
			"down_severity":      str(descDownSeverity),
			"late_severity":      str(descLateSeverity),
			"status":             str(descStatus),
			"paused":             boolean(descPaused),
			"ping_token": schema.StringAttribute{
				MarkdownDescription: descPingToken,
				Computed:            true,
				Sensitive:           true,
			},
			"ping_url": schema.StringAttribute{
				MarkdownDescription: "Full heartbeat URL. Null whenever `ping_token` is null. Sensitive.",
				Computed:            true,
				Sensitive:           true,
			},
			"created_at": str(descCreatedAt),
			"updated_at": str(descUpdatedAt),
		},
	}
}

func (d *jobDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug")),
	}
}

func (d *jobDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *jobDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg jobModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := lookupRef(cfg.ID, cfg.Slug)
	out, err := d.client.getJob(ctx, ref)
	if err != nil {
		addAPIError(&resp.Diagnostics, "read Ionhour job "+ref, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, jobFromAPI(*out, nil))...)
}
