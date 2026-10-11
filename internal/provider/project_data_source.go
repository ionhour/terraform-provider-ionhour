package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

var (
	_ datasource.DataSource                     = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*projectDataSource)(nil)
)

// NewProjectDataSource is the ionhour_project data source factory.
func NewProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDataSource struct {
	client *apiClient
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an Ionhour project by `id` or exact `name` (exactly one). " +
			"A name shared by several projects is an error; use the id then.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Numeric project id. Set exactly one of `id` and `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Exact project name. Set exactly one of `id` and `name`.",
				Optional:            true,
				Computed:            true,
			},
			"description": schema.StringAttribute{MarkdownDescription: "Project description.", Computed: true},
			"created_at":  schema.StringAttribute{MarkdownDescription: descCreatedAt, Computed: true},
			"updated_at":  schema.StringAttribute{MarkdownDescription: descUpdatedAt, Computed: true},
		},
	}
}

func (d *projectDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg projectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(cfg.ID) {
		out, err := d.client.getProject(ctx, idRef(cfg.ID))
		if err != nil {
			addAPIError(&resp.Diagnostics, "read Ionhour project "+idRef(cfg.ID), err)
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(*out))...)
		return
	}

	name := cfg.Name.ValueString()
	matches, err := d.client.listProjectsByName(ctx, name)
	if err != nil {
		addAPIError(&resp.Diagnostics, "list Ionhour projects", err)
		return
	}
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Ionhour project not found",
			fmt.Sprintf("No project named %q in this workspace.", name))
	case 1:
		resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(matches[0]))...)
	default:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous Ionhour project name",
			fmt.Sprintf("%d projects are named %q. Look the project up by id instead.", len(matches), name))
	}
}
