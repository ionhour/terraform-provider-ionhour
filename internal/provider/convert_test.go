package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"
)

func TestParseImportID(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "123", want: "123"},
		{in: " 42 ", want: "42"},
		{in: "slug:public-api", want: "slug:public-api"},
		{in: "slug:a_b-1", want: "slug:a_b-1"},
		{in: "slug:Public", wantErr: true},
		{in: "slug:", wantErr: true},
		{in: "0", wantErr: true},
		{in: "-3", wantErr: true},
		{in: "public-api", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseImportID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseImportID(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseImportID(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestLookupRef(t *testing.T) {
	if got := lookupRef(types.Int64Value(7), types.StringNull()); got != "7" {
		t.Errorf("by id = %q", got)
	}
	if got := lookupRef(types.Int64Null(), types.StringValue("nightly")); got != "slug:nightly" {
		t.Errorf("by slug = %q", got)
	}
}

func TestLowercaseKeysAndCollisions(t *testing.T) {
	in := map[string]string{"X-Api-Key": "a", "Accept": "b"}
	want := map[string]string{"x-api-key": "a", "accept": "b"}
	if got := lowercaseKeys(in); !reflect.DeepEqual(got, want) {
		t.Errorf("lowercaseKeys = %v, want %v", got, want)
	}
	if in["X-Api-Key"] != "a" || len(in) != 2 {
		t.Errorf("lowercaseKeys mutated its input: %v", in)
	}
	if lowercaseKeys(nil) != nil {
		t.Error("lowercaseKeys(nil) must stay nil")
	}
	got := headerKeyCollisions(map[string]string{"X-A": "1", "x-a": "2", "x-b": "3"})
	if !reflect.DeepEqual(got, []string{"X-A", "x-a"}) {
		t.Errorf("headerKeyCollisions = %v", got)
	}
	if got := headerKeyCollisions(map[string]string{"x-a": "1"}); got != nil {
		t.Errorf("no collisions expected, got %v", got)
	}
}

func TestReconcileHeaders(t *testing.T) {
	server := map[string]string{"x-api-key": "s"}
	tests := []struct {
		name  string
		prior map[string]string
		want  map[string]string
	}{
		{"no prior keeps server", nil, server},
		{"configured case is kept", map[string]string{"X-Api-Key": "s"}, map[string]string{"X-Api-Key": "s"}},
		{"value drift shows server", map[string]string{"X-Api-Key": "old"}, server},
		{"extra key drift shows server", map[string]string{"X-Api-Key": "s", "X-B": "1"}, server},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconcileHeaders(server, tt.prior); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reconcileHeaders = %v, want %v", got, tt.want)
			}
		})
	}
	if reconcileHeaders(nil, map[string]string{"a": "b"}) != nil {
		t.Error("a server null must stay null")
	}
}

// marshalNullable renders a nullable the way the generated DTOs do (inside an
// omitempty field), so tests read like the wire format.
func marshalNullable[T any](t *testing.T, n nullable.Nullable[T]) string {
	t.Helper()
	b, err := json.Marshal(struct {
		V nullable.Nullable[T] `json:"v,omitempty"`
	}{n})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPatchNullable(t *testing.T) {
	val := func(v types.Int64) func() int { return int64Value(v) }
	tests := []struct {
		name             string
		plan, state, cfg types.Int64
		wantSent         bool
		wantJSON         string
	}{
		{"unchanged", types.Int64Value(5), types.Int64Value(5), types.Int64Value(5), false, `{}`},
		{"unknown plan", types.Int64Unknown(), types.Int64Value(5), types.Int64Unknown(), false, `{}`},
		{"changed", types.Int64Value(7), types.Int64Value(5), types.Int64Value(7), true, `{"v":7}`},
		{"removed nullable", types.Int64Null(), types.Int64Value(5), types.Int64Null(), true, `{"v":null}`},
		{"removed defaulted resets with null", types.Int64Value(8000), types.Int64Value(5000), types.Int64Null(), true, `{"v":null}`},
		{"set from null", types.Int64Value(3), types.Int64Null(), types.Int64Value(3), true, `{"v":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, sent := patchNullable(tt.plan, tt.state, tt.cfg, val(tt.plan))
			if sent != tt.wantSent {
				t.Fatalf("sent = %v, want %v", sent, tt.wantSent)
			}
			if j := marshalNullable(t, got); j != tt.wantJSON {
				t.Errorf("json = %s, want %s", j, tt.wantJSON)
			}
		})
	}
}

func TestCreateNullable(t *testing.T) {
	for _, tt := range []struct {
		v    types.String
		want string
	}{
		{types.StringNull(), `{}`},
		{types.StringUnknown(), `{}`},
		{types.StringValue("x"), `{"v":"x"}`},
	} {
		if got := marshalNullable(t, createNullable(tt.v, tt.v.ValueString)); got != tt.want {
			t.Errorf("createNullable(%v) = %s, want %s", tt.v, got, tt.want)
		}
	}
}

func TestNullableToTerraform(t *testing.T) {
	checks := []struct {
		name string
		got  attr.Value
		want attr.Value
	}{
		{"unspecified string", nullableString(nullable.Nullable[string]{}), types.StringNull()},
		{"null string", nullableString(nullable.NewNullNullable[string]()), types.StringNull()},
		{"string", nullableString(nullable.NewNullableWithValue("a")), types.StringValue("a")},
		{"null int", nullableInt64(nullable.NewNullNullable[int]()), types.Int64Null()},
		{"int", nullableInt64(nullable.NewNullableWithValue(3)), types.Int64Value(3)},
		{"null bool", nullableBool(nullable.NewNullNullable[bool]()), types.BoolNull()},
		{"bool", nullableBool(nullable.NewNullableWithValue(false)), types.BoolValue(false)},
	}
	for _, c := range checks {
		if !c.got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestNullableOr(t *testing.T) {
	if got := nullableOr(nullable.NewNullableWithValue("ADMIN"), "none"); got != "ADMIN" {
		t.Errorf("value = %q", got)
	}
	if got := nullableOr(nullable.NewNullNullable[string](), "none"); got != "none" {
		t.Errorf("null = %q", got)
	}
	if got := nullableOr(nullable.Nullable[string]{}, "none"); got != "none" {
		t.Errorf("absent = %q", got)
	}
}
