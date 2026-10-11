package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

var (
	_ datasource.DataSource                     = (*checkDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*checkDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*checkDataSource)(nil)
)

// NewCheckDataSource is the ionhour_check data source factory.
func NewCheckDataSource() datasource.DataSource { return &checkDataSource{} }

type checkDataSource struct {
	client *apiClient
}

func (d *checkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_check"
}

func (d *checkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = checkDataSourceSchema()
}

func (d *checkDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug")),
	}
}

func (d *checkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *checkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg checkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := lookupRef(cfg.ID, cfg.Slug)
	out, err := d.client.getCheck(ctx, ref)
	if err != nil {
		addAPIError(&resp.Diagnostics, "read Ionhour check "+ref, err)
		return
	}
	state, diags := checkFromAPI(ctx, *out, nil)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
