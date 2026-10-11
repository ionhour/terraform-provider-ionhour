package provider

import (
	"context"
	"errors"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ionhour/go-sdk/api"
)

func isNumericRef(ref string) bool {
	_, err := parseNumericRef(ref)
	return err == nil
}

func parseNumericRef(ref string) (int64, error) { return strconv.ParseInt(ref, 10, 64) }

// importByIDOrSlug implements `terraform import` for resources addressable by
// a numeric id or `slug:<slug>`. The reference is resolved to the numeric id
// once, so state always tracks the immutable id (slugs can change or be
// released). lookup may reject a resource the provider cannot manage.
func importByIDOrSlug(ctx context.Context, kind string, req resource.ImportStateRequest,
	resp *resource.ImportStateResponse, lookup func(ctx context.Context, ref string) (int64, error)) {
	ref, err := parseImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	// Always resolve through the API: a slug becomes its id, and a numeric id
	// is checked to exist and be manageable before it lands in state.
	id, err := lookup(ctx, ref)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			addAPIError(&resp.Diagnostics, "import Ionhour "+kind+" "+ref, err)
		} else {
			resp.Diagnostics.AddError("Cannot import Ionhour "+kind+" "+ref, err.Error())
		}
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.Int64Value(id))...)
}

// lookupRef is the :id path parameter for a data source configured with
// exactly one of a numeric id or a slug.
func lookupRef(id types.Int64, slug types.String) string {
	if known(id) {
		return idRef(id)
	}
	return slugPrefix + slug.ValueString()
}
