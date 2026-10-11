package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ionhour/go-sdk/api"
)

var (
	_ resource.Resource                   = (*checkResource)(nil)
	_ resource.ResourceWithConfigure      = (*checkResource)(nil)
	_ resource.ResourceWithImportState    = (*checkResource)(nil)
	_ resource.ResourceWithValidateConfig = (*checkResource)(nil)
)

// NewCheckResource is the ionhour_check factory.
func NewCheckResource() resource.Resource { return &checkResource{} }

type checkResource struct {
	client *apiClient
}

func (r *checkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_check"
}

func (r *checkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = checkResourceSchema()
}

func (r *checkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, resp.Diagnostics.AddError)
}

// ValidateConfig catches at plan time what the API would reject with 422.
// Only the config object is decoded, and only when it is known, so a config
// built from values known after apply never fails validation.
func (r *checkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var obj types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("config"), &obj)...)
	if resp.Diagnostics.HasError() || !known(obj) {
		return
	}
	var cfg checkConfigModel
	resp.Diagnostics.Append(obj.As(ctx, &cfg, basetypes.ObjectAsOptions{})...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateHTTPConfig(ctx, cfg)...)
}

func validateHTTPConfig(_ context.Context, c checkConfigModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if known(c.ExpectedStatusMin) && known(c.ExpectedStatusMax) &&
		c.ExpectedStatusMin.ValueInt64() > c.ExpectedStatusMax.ValueInt64() {
		diags.AddAttributeError(path.Root("config").AtName("expected_status_min"), "Invalid status range",
			fmt.Sprintf("expected_status_min (%d) must not exceed expected_status_max (%d).",
				c.ExpectedStatusMin.ValueInt64(), c.ExpectedStatusMax.ValueInt64()))
	}
	// Only a null max takes the default; an unknown max is decided at apply.
	if c.ExpectedStatusMax.IsNull() && known(c.ExpectedStatusMin) && c.ExpectedStatusMin.ValueInt64() > defaultExpectedStatusMax {
		diags.AddAttributeError(path.Root("config").AtName("expected_status_min"), "Invalid status range",
			fmt.Sprintf("expected_status_min (%d) exceeds the default expected_status_max (%d); set expected_status_max too.",
				c.ExpectedStatusMin.ValueInt64(), defaultExpectedStatusMax))
	}

	// Header NAMES are always known in configuration even when values come
	// from resources created in the same apply (random_password, ...), so
	// validation reads only the map keys.
	if !known(c.CustomHeaders) {
		return diags
	}
	headers := make(map[string]string, len(c.CustomHeaders.Elements()))
	for name := range c.CustomHeaders.Elements() {
		headers[name] = ""
	}
	headersPath := path.Root("config").AtName("custom_headers")
	if collisions := headerKeyCollisions(headers); len(collisions) > 0 {
		diags.AddAttributeError(headersPath, "Duplicate header names",
			"Header names are case-insensitive and are sent lowercased; these collide: "+strings.Join(collisions, ", ")+".")
	}
	var reserved []string
	for name := range headers {
		lower := strings.ToLower(name)
		if reservedHeaderNames[lower] || strings.HasPrefix(lower, "proxy-") || strings.HasPrefix(lower, "x-forwarded-") {
			reserved = append(reserved, name)
		}
	}
	if len(reserved) > 0 {
		sort.Strings(reserved)
		diags.AddAttributeError(headersPath, "Reserved header names",
			"The probe controls these headers and the API rejects them: "+strings.Join(reserved, ", ")+".")
	}
	return diags
}

func (r *checkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg checkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := checkCreateBody(ctx, cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.createCheck(ctx, body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "create Ionhour check", err)
		return
	}
	state, diags := checkFromAPI(ctx, *out, &plan)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *checkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior checkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.getCheck(ctx, idRef(prior.ID))
	if api.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "read Ionhour check", err)
		return
	}
	if err := requireHTTPCheck(out); err != nil {
		resp.Diagnostics.AddError("Unsupported Ionhour check type", err.Error())
		return
	}
	state, diags := checkFromAPI(ctx, *out, &prior)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *checkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior, cfg checkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := checkPatchBody(ctx, plan, prior, cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.client.updateCheck(ctx, idRef(prior.ID), body)
	if err != nil {
		addAPIError(&resp.Diagnostics, "update Ionhour check", err)
		return
	}
	state, diags := checkFromAPI(ctx, *out, &plan)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *checkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state checkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.deleteCheck(ctx, idRef(state.ID)); err != nil && !api.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "delete Ionhour check", err)
	}
}

// ImportState accepts a numeric id or `slug:<slug>`.
func (r *checkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importByIDOrSlug(ctx, "check", req, resp, func(ctx context.Context, ref string) (int64, error) {
		out, err := r.client.getCheck(ctx, ref)
		if err != nil {
			return 0, err
		}
		if err := requireHTTPCheck(out); err != nil {
			return 0, err
		}
		return int64(out.Id), nil
	})
}

// requireHTTPCheck rejects checks the resource cannot manage. v0.1 manages
// HTTP checks only (other types answer 422 on PATCH); failing here is clearer
// than planning a replacement that would destroy a dashboard-managed check.
func requireHTTPCheck(c *api.CheckV1Response) error {
	if c.CheckType == api.CheckTypeHTTP {
		return nil
	}
	return fmt.Errorf("check %d is a %s check. HTTP checks only in v0.1: ionhour_check manages and imports "+
		"HTTP checks only. Read other check types with the ionhour_check data source, and remove this one "+
		"from state with `terraform state rm` if it was imported", c.Id, c.CheckType)
}
