package provider

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/ionhour/go-sdk/api"
)

// Stable Integration API error codes the provider gives specific guidance
// for. Any other code is still surfaced verbatim.
const (
	codeSlugTaken             = "SLUG_TAKEN"
	codeProjectNotEmpty       = "PROJECT_NOT_EMPTY"
	codePlanLimitExceeded     = "PLAN_LIMIT_EXCEEDED"
	codeCheckTypeUnsupported  = "CHECK_TYPE_NOT_SUPPORTED"
	codeIdempotencyReused     = "IDEMPOTENCY_KEY_REUSED"
	codeIdempotencyInProgress = "IDEMPOTENCY_KEY_IN_PROGRESS"
	codeValidationFailed      = "VALIDATION_FAILED"
	codeNotFound              = "NOT_FOUND"
	codeRateLimitedKey        = "RATE_LIMIT_EXCEEDED_KEY"
	codeServiceKeyInactive    = "SERVICE_KEY_INACTIVE"
	codeKeyReadOnly           = "KEY_READ_ONLY"
	codeKeyScopeDenied        = "KEY_SCOPE_DENIED"
	codeInsufficientRole      = "INSUFFICIENT_ROLE"
	codeInvalidKey            = "INVALID_KEY"
	codeKeyExpired            = "KEY_EXPIRED"
)

var codeHints = map[string]string{
	codeSlugTaken: "Another check or job in this workspace already uses this slug. Choose a different " +
		"slug, or adopt the existing resource with `terraform import` (ID `slug:<slug>`).",
	codeProjectNotEmpty: "The project still has active checks or jobs. The API never cascades a project " +
		"delete: remove or move its monitors first (including ones created outside Terraform).",
	codePlanLimitExceeded: "The workspace plan does not allow this. Raise the plan or relax the " +
		"configuration (interval floor, monitor count, regions, or a plan-gated field).",
	codeCheckTypeUnsupported: "The Integration API v1 manages HTTP checks only. Other check types stay " +
		"dashboard-managed until their API vertical ships.",
	codeIdempotencyReused: "The same Idempotency-Key was replayed with a different body. This indicates " +
		"a provider bug; re-run the apply to mint a new key.",
	codeIdempotencyInProgress: "An earlier attempt of this create is still running on the server. Run " +
		"`terraform apply` again; if the resource was created, import it instead of creating a duplicate.",
	codeRateLimitedKey: "The API key exceeded its integration rate limit. Lower -parallelism or retry " +
		"after the Retry-After interval.",
	codeServiceKeyInactive: "The SERVICE key is disabled, expired, missing its role, or the workspace " +
		"cannot use service keys. Check the key in Workspace settings > API tokens.",
	codeKeyReadOnly: "The API key is READ_ONLY. Use a READ_WRITE key for resources.",
	codeKeyScopeDenied: "The API key does not cover this resource. Allow mcp:projects, mcp:checks and " +
		"mcp:jobs on the key.",
	codeInsufficientRole: "The key's role is too low for this operation. Creates and updates need MEMBER; " +
		"deletes need ADMIN.",
	codeInvalidKey: "The API key is not valid for this base_url. SIT/UAT keys only work with their own " +
		"environment's base_url.",
	codeKeyExpired: "The API key has expired. Create a new key and update api_key / IONHOUR_API_KEY.",
}

// addAPIError appends an error diagnostic for a failed API call. action is a
// verb phrase such as "create Ionhour check".
func addAPIError(diags *diag.Diagnostics, action string, err error) {
	summary, detail := describeAPIError(action, err)
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Code == codeSlugTaken {
		diags.AddAttributeError(path.Root("slug"), summary, detail)
		return
	}
	diags.AddError(summary, detail)
}

// describeAPIError renders the summary and detail of an API failure. Pure so
// the wording is unit-testable.
func describeAPIError(action string, err error) (string, string) {
	summary := "Unable to " + action
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return summary, fmt.Sprintf("Request to the Ionhour API failed: %s", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The Ionhour API answered HTTP %d with code %s", apiErr.Status, apiErr.Code)
	if apiErr.Message != "" {
		fmt.Fprintf(&b, ": %s", apiErr.Message)
	}
	b.WriteString(".")
	if len(apiErr.Messages) > 1 {
		b.WriteString("\n")
		for _, m := range apiErr.Messages {
			fmt.Fprintf(&b, "\n  - %s", m)
		}
	}
	if len(apiErr.Detail) > 0 {
		fmt.Fprintf(&b, "\n\nDetail: %s", formatDetail(apiErr.Detail))
	}
	if apiErr.RetryAfter > 0 {
		fmt.Fprintf(&b, "\n\nRetry after %s.", apiErr.RetryAfter)
	}
	if hint, ok := codeHints[apiErr.Code]; ok {
		fmt.Fprintf(&b, "\n\n%s", hint)
	}
	return summary + " (" + apiErr.Code + ")", b.String()
}

// formatDetail prints a detail object with sorted keys so diagnostics are
// stable across runs.
func formatDetail(detail map[string]any) string {
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, detail[k]))
	}
	return strings.Join(parts, ", ")
}
