package provider

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

// slugPattern is the server's slug rule for checks and jobs.
var slugPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

const slugPrefix = "slug:"

// ---- API -> Terraform ----

func nullableString(n nullable.Nullable[string]) types.String {
	if !n.IsSpecified() || n.IsNull() {
		return types.StringNull()
	}
	return types.StringValue(n.MustGet())
}

func nullableInt64(n nullable.Nullable[int]) types.Int64 {
	if !n.IsSpecified() || n.IsNull() {
		return types.Int64Null()
	}
	return types.Int64Value(int64(n.MustGet()))
}

func nullableBool(n nullable.Nullable[bool]) types.Bool {
	if !n.IsSpecified() || n.IsNull() {
		return types.BoolNull()
	}
	return types.BoolValue(n.MustGet())
}

func nullableMethod(n nullable.Nullable[api.OutboundHttpMethod]) types.String {
	if !n.IsSpecified() || n.IsNull() {
		return types.StringNull()
	}
	return types.StringValue(string(n.MustGet()))
}

// timestamp renders API times as RFC 3339 UTC with second precision.
func timestamp(t time.Time) types.String {
	if t.IsZero() {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// ---- Terraform -> API ----

func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

func intPtr(v types.Int64) *int {
	if !known(v) {
		return nil
	}
	i := int(v.ValueInt64())
	return &i
}

func boolPtr(v types.Bool) *bool {
	if !known(v) {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func stringPtr(v types.String) *string {
	if !known(v) {
		return nil
	}
	s := v.ValueString()
	return &s
}

func severityPtr(v types.String) *api.IncidentSeverity {
	if !known(v) {
		return nil
	}
	s := api.IncidentSeverity(v.ValueString())
	return &s
}

// createNullable is the create-time encoding: a null or unknown value is
// omitted (on create, null equals omission), a known value is sent.
func createNullable[T any](v attr.Value, value func() T) nullable.Nullable[T] {
	if !known(v) {
		return nullable.Nullable[T]{}
	}
	return nullable.NewNullableWithValue(value())
}

// patchNullable is the PATCH encoding for a single attribute. It sends
// nothing when the planned value equals the prior state (or is unknown), an
// explicit null when the attribute was removed from configuration (the server
// clears nullable fields and resets defaulted ones), and the planned value
// otherwise.
func patchNullable[T any](plan, state, config attr.Value, value func() T) (nullable.Nullable[T], bool) {
	if plan.IsUnknown() || plan.Equal(state) {
		return nullable.Nullable[T]{}, false
	}
	if plan.IsNull() || config.IsNull() {
		return nullable.NewNullNullable[T](), true
	}
	return nullable.NewNullableWithValue(value()), true
}

// ---- collections ----

func stringMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	if !known(m) {
		return nil, nil
	}
	out := map[string]string{}
	diags := m.ElementsAs(ctx, &out, false)
	return out, diags
}

func stringSet(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if !known(s) {
		return nil, nil
	}
	var out []string
	diags := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

func mapValue(ctx context.Context, m map[string]string) (types.Map, diag.Diagnostics) {
	if m == nil {
		return types.MapNull(types.StringType), nil
	}
	return types.MapValueFrom(ctx, types.StringType, m)
}

func setValue(ctx context.Context, s []string) (types.Set, diag.Diagnostics) {
	if s == nil {
		return types.SetNull(types.StringType), nil
	}
	return types.SetValueFrom(ctx, types.StringType, s)
}

// ---- custom headers ----

// lowercaseKeys returns a copy of m with every key lowercased, which is what
// the server stores and returns.
func lowercaseKeys(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// headerKeyCollisions lists the keys that become equal after lowercasing
// (the server rejects them), sorted for stable diagnostics.
func headerKeyCollisions(m map[string]string) []string {
	seen := map[string][]string{}
	for k := range m {
		lk := strings.ToLower(k)
		seen[lk] = append(seen[lk], k)
	}
	var out []string
	for _, ks := range seen {
		if len(ks) > 1 {
			out = append(out, ks...)
		}
	}
	sort.Strings(out)
	return out
}

// reconcileHeaders keeps the configured spelling of header names: when the
// server's (lowercased) map is the lowercased form of prior, prior is
// returned unchanged so mixed-case configuration never shows a diff.
func reconcileHeaders(server, prior map[string]string) map[string]string {
	if server == nil {
		return nil
	}
	if prior != nil && maps.Equal(lowercaseKeys(prior), server) {
		return prior
	}
	return server
}

// ---- ids ----

// idRef is the :id path parameter for a numeric id.
func idRef(id types.Int64) string { return strconv.FormatInt(id.ValueInt64(), 10) }

// parseImportID accepts a numeric id or `slug:<slug>` and returns the :id
// path parameter to resolve it with.
func parseImportID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if slug, ok := strings.CutPrefix(raw, slugPrefix); ok {
		if !slugPattern.MatchString(slug) {
			return "", fmt.Errorf("invalid slug %q: want %s", slug, slugPattern)
		}
		return slugPrefix + slug, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("invalid import ID %q: want a numeric id or slug:<slug>", raw)
	}
	return strconv.FormatInt(n, 10), nil
}
