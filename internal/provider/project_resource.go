package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ionhour/go-sdk/api"
)

var (
	_ resource.Resource                = (*projectResource)(nil)
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
)

// NewProjectResource is the ionhour_project factory.
func NewProjectResource() resource.Resource { return &projectResource{} }

type projectResource struct {
	client *apiClient
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An Ionhour project: the container for checks and jobs.\n\n" +
			"Deleting a project that still has active checks or jobs fails with `PROJECT_NOT_EMPTY`; " +
			"the API never cascades, so a project replacement cannot wipe monitors created in the dashboard.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Numeric project id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Project name (1-255 characters).",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-text description. Removing it clears the stored value.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(255)},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Creation time (RFC 3339, UTC).",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "Last update time (RFC 3339, UTC).",
				Computed:            true,
			},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var cfg projectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.createProject(ctx, projectCreateBody(cfg))
	if err != nil {
		addAPIError(&resp.Diagnostics, "create Ionhour project", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(*out))...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.getProject(ctx, idRef(state.ID))
	if api.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "read Ionhour project", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(*out))...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.updateProject(ctx, idRef(state.ID), projectPatchBody(plan, state))
	if err != nil {
		addAPIError(&resp.Diagnostics, "update Ionhour project", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(*out))...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.deleteProject(ctx, idRef(state.ID)); err != nil && !api.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "delete Ionhour project", err)
	}
}

// ImportState accepts the numeric project id (projects have no slug).
func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref, err := parseImportID(req.ID)
	if err != nil || !isNumericRef(ref) {
		resp.Diagnostics.AddError("Invalid import ID", "Import an ionhour_project by its numeric id.")
		return
	}
	id, _ := parseNumericRef(ref)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.Int64Value(id))...)
}
