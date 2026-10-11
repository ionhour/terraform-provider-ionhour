package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ionhour/go-sdk/api"
)

var (
	_ datasource.DataSource              = (*regionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*regionsDataSource)(nil)
)

// NewRegionsDataSource is the ionhour_regions data source factory.
func NewRegionsDataSource() datasource.DataSource { return &regionsDataSource{} }

type regionsDataSource struct {
	client *apiClient
}

type regionsModel struct {
	Regions types.List `tfsdk:"regions"`
	IDs     types.List `tfsdk:"ids"`
}

var regionAttrTypes = map[string]attr.Type{
	"id":           types.StringType,
	"display_name": types.StringType,
	"flag":         types.StringType,
	"enabled":      types.BoolType,
}

func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The probe regions this Ionhour environment serves, for `ionhour_check.config.regions`.",
		Attributes: map[string]schema.Attribute{
			"regions": schema.ListNestedAttribute{
				MarkdownDescription: "Every active region, in API order.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":           schema.StringAttribute{MarkdownDescription: "Region id, as used in `config.regions`.", Computed: true},
						"display_name": schema.StringAttribute{MarkdownDescription: "Human-readable name.", Computed: true},
						"flag":         schema.StringAttribute{MarkdownDescription: "Flag emoji.", Computed: true},
						"enabled":      schema.BoolAttribute{MarkdownDescription: "Whether checks can use the region.", Computed: true},
					},
				},
			},
			"ids": schema.ListAttribute{
				MarkdownDescription: "Ids of the enabled regions, in API order.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (d *regionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *regionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	regions, err := d.client.listRegions(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "list Ionhour regions", err)
		return
	}
	state, diags := regionsFromAPI(ctx, regions)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func regionsFromAPI(ctx context.Context, regions []api.RegionV1Response) (regionsModel, diag.Diagnostics) {
	type region struct {
		ID          string `tfsdk:"id"`
		DisplayName string `tfsdk:"display_name"`
		Flag        string `tfsdk:"flag"`
		Enabled     bool   `tfsdk:"enabled"`
	}
	all := make([]region, 0, len(regions))
	ids := make([]string, 0, len(regions))
	for _, r := range regions {
		all = append(all, region{ID: r.Id, DisplayName: r.DisplayName, Flag: r.Flag, Enabled: r.Enabled})
		if r.Enabled {
			ids = append(ids, r.Id)
		}
	}
	var diags diag.Diagnostics
	list, d := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: regionAttrTypes}, all)
	diags.Append(d...)
	idList, d := types.ListValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
	return regionsModel{Regions: list, IDs: idList}, diags
}
