package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ionhour/go-sdk/api"
)

// projectModel is the state of ionhour_project and the ionhour_project data
// source (same attribute names).
type projectModel struct {
	ID          types.Int64  `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

func projectCreateBody(cfg projectModel) api.CreateProjectV1Dto {
	return api.CreateProjectV1Dto{
		Name:        cfg.Name.ValueString(),
		Description: createNullable(cfg.Description, cfg.Description.ValueString),
	}
}

// projectPatchBody sends only what changed; a removed description becomes an
// explicit null, which clears it.
func projectPatchBody(plan, state projectModel) api.UpdateProjectV1Dto {
	var body api.UpdateProjectV1Dto
	if !plan.Name.Equal(state.Name) {
		body.Name = stringPtr(plan.Name)
	}
	if d, ok := patchNullable(plan.Description, state.Description, plan.Description, plan.Description.ValueString); ok {
		body.Description = d
	}
	return body
}

func projectFromAPI(p api.ProjectV1Response) projectModel {
	return projectModel{
		ID:          types.Int64Value(int64(p.Id)),
		Name:        types.StringValue(p.Name),
		Description: nullableString(p.Description),
		CreatedAt:   timestamp(p.CreatedAt),
		UpdatedAt:   timestamp(p.UpdatedAt),
	}
}
