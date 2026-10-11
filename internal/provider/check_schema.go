package provider

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	severities  = []string{"P1", "P2", "P3", "P4"}
	httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH"}
	httpURL     = regexp.MustCompile(`^https?://`)
)

// Attribute descriptions shared by the resource and data source schemas.
const (
	descCheckSlug = "Unique per workspace (`^[a-z0-9_-]{1,64}$`). Addresses the check as `slug:<slug>` " +
		"for import and lookups. Removing it clears the slug; deleting the check releases it."
	descProjectID      = "Project the monitor belongs to. Changing it moves the monitor in place (same id)."
	descCheckType      = "Probe type. Only `HTTP` is managed through the API in v1."
	descCheckInterval  = "Probe interval in seconds (10-3600, plan floor applies)."
	descCheckGrace     = "Grace period in seconds (5-60)."
	descMute           = "Suppress notifications. Setting `true` on a check is plan-gated."
	descDownSeverity   = "Incident severity when the monitor goes DOWN (`P1`-`P4`)."
	descLateSeverity   = "Incident severity when the monitor goes LATE (`P1`-`P4`)."
	descStatus         = "Current status: `NEW`, `OK`, `LATE`, `DOWN`, `PAUSED` or `RESUMED`."
	descPaused         = "Whether the monitor is paused (status `PAUSED`). Pause and resume are not managed by Terraform."
	descCreatedAt      = "Creation time (RFC 3339, UTC)."
	descUpdatedAt      = "Last update time (RFC 3339, UTC)."
	descURL            = "Target URL (http or https, at most 2048 characters). Immutable: changing it replaces the check."
	descMethod         = "HTTP method: `GET`, `HEAD`, `POST`, `PUT` or `PATCH`."
	descCustomHeaders  = "Request headers (at most 10, values up to 2048 characters). Names are sent lowercased; a configured spelling is kept in state. Names that collide after lowercasing and reserved names (`host`, `authorization`, `cookie`, `connection`, `content-length`, `transfer-encoding`, `x-forwarded-*`, `proxy-*`) are rejected. Removing it clears the headers. Plan-gated. Sensitive."
	descTimeout        = "Probe timeout in milliseconds (1000-15000)."
	descStatusMin      = "Lowest passing HTTP status, inclusive (100-599). Must not exceed `expected_status_max`."
	descStatusMax      = "Highest passing HTTP status, inclusive (100-599)."
	descRegions        = "Probe region ids (1-6, see the `ionhour_regions` data source). When never set, the check runs from the environment's default regions, which are recorded in state. Removing it from configuration keeps the current regions; list them explicitly to change them. The count is plan-limited."
	descFollow         = "Follow redirects."
	descMaxRedirects   = "Maximum redirects to follow (0-3)."
	descBlockDowngrade = "Fail when a redirect downgrades HTTPS to HTTP."
	descFailAfter      = "Consecutive failures before the check goes DOWN (1-10)."
	descResolveAfter   = "Consecutive successes before an incident resolves (1-10)."
	descLatencyWarn    = "Latency alert threshold in milliseconds. Removing it disables the alert. Plan-gated."
	descSSLExpiry      = "TLS certificate expiry alert threshold in days (1-365). Setting a non-default value is plan-gated."
	descBodyContains   = "Pass only if the response body contains this text (case-insensitive, at most 1000 characters). Plan-gated."
	descBodyNot        = "Fail if the response body contains this text (case-insensitive, at most 1000 characters). Plan-gated."
	descIPFamily       = "IP family the probe uses (`AUTO`, `IPV4`, `IPV6`). Read-only."
	descSkipTLS        = "Whether TLS verification is skipped. Read-only."
	descMaxResponse    = "Largest response body the probe reads, in bytes. Read-only."
)

// reservedHeaderNames are rejected by the API (422). Checked client-side so
// the error shows at plan time.
var reservedHeaderNames = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true,
	"cookie": true, "authorization": true, "x-forwarded-for": true, "x-forwarded-host": true,
	"x-forwarded-proto": true, "x-forwarded-port": true, "proxy-authorization": true,
	"proxy-connection": true, "__proto__": true, "constructor": true, "prototype": true,
}

func severityValidators() []validator.String {
	return []validator.String{stringvalidator.OneOf(severities...)}
}

func slugValidators() []validator.String {
	return []validator.String{stringvalidator.RegexMatches(slugPattern, "must match ^[a-z0-9_-]{1,64}$")}
}

func checkResourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "An outbound HTTP check: Ionhour probes `config.url` on a schedule from one or more regions.\n\n" +
			"Attributes the API defaults carry the same default here, so leaving them out never produces a diff, " +
			"and removing one resets it. Creates carry an auto-generated `Idempotency-Key`, so a retried apply " +
			"never duplicates a check. Import by numeric id or `slug:<slug>`.\n\n" +
			"~> **HTTP checks only in v0.1.** Reading or importing a TCP, DNS, SSL or heartbeat check fails " +
			"with an error instead of planning a replacement. Read those with the `ionhour_check` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Numeric check id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Check name (1-255 characters).",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: descCheckSlug,
				Optional:            true,
				Validators:          slugValidators(),
			},
			"project_id": schema.Int64Attribute{
				MarkdownDescription: descProjectID,
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"check_type": schema.StringAttribute{
				MarkdownDescription: descCheckType + " Changing it replaces the check.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultCheckType),
				Validators:          []validator.String{stringvalidator.OneOf(defaultCheckType)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"interval_seconds": schema.Int64Attribute{
				MarkdownDescription: descCheckInterval + " Defaults to `300`.",
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(defaultCheckIntervalSeconds),
				Validators:          []validator.Int64{int64validator.Between(10, 3600)},
			},
			"grace_seconds": schema.Int64Attribute{
				MarkdownDescription: descCheckGrace + " Defaults to `5`.",
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(defaultCheckGraceSeconds),
				Validators:          []validator.Int64{int64validator.Between(5, 60)},
			},
			"mute_notifications": schema.BoolAttribute{
				MarkdownDescription: descMute + " Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"down_severity": schema.StringAttribute{
				MarkdownDescription: descDownSeverity + " Defaults to `P1`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultDownSeverity),
				Validators:          severityValidators(),
			},
			"late_severity": schema.StringAttribute{
				MarkdownDescription: descLateSeverity + " Defaults to `P3`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultLateSeverity),
				Validators:          severityValidators(),
			},
			"config": schema.SingleNestedAttribute{
				MarkdownDescription: "HTTP probe configuration.",
				Required:            true,
				Attributes:          httpConfigResourceAttributes(),
			},
			"status": schema.StringAttribute{
				MarkdownDescription: descStatus,
				Computed:            true,
			},
			"paused": schema.BoolAttribute{
				MarkdownDescription: descPaused,
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: descCreatedAt,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: descUpdatedAt,
				Computed:            true,
			},
		},
	}
}

func optionalInt64(desc string, def int64, v ...validator.Int64) schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		Default:             int64default.StaticInt64(def),
		Validators:          v,
	}
}

func optionalBool(desc string, def bool) schema.BoolAttribute {
	return schema.BoolAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		Default:             booldefault.StaticBool(def),
	}
}

func httpConfigResourceAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"url": schema.StringAttribute{
			MarkdownDescription: descURL,
			Required:            true,
			Validators: []validator.String{
				stringvalidator.LengthBetween(1, 2048),
				stringvalidator.RegexMatches(httpURL, "must be an http:// or https:// URL"),
			},
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"method": schema.StringAttribute{
			MarkdownDescription: descMethod + " Defaults to `GET`.",
			Optional:            true,
			Computed:            true,
			Default:             stringdefault.StaticString(defaultMethod),
			Validators:          []validator.String{stringvalidator.OneOf(httpMethods...)},
		},
		"custom_headers": schema.MapAttribute{
			MarkdownDescription: descCustomHeaders,
			ElementType:         types.StringType,
			Optional:            true,
			Sensitive:           true,
			Validators: []validator.Map{
				mapvalidator.SizeAtMost(10),
				mapvalidator.ValueStringsAre(stringvalidator.LengthAtMost(2048)),
			},
		},
		"timeout_ms":          optionalInt64(descTimeout+" Defaults to `8000`.", defaultTimeoutMs, int64validator.Between(1000, 15000)),
		"expected_status_min": optionalInt64(descStatusMin+" Defaults to `200`.", defaultExpectedStatusMin, int64validator.Between(100, 599)),
		"expected_status_max": optionalInt64(descStatusMax+" Defaults to `399`.", defaultExpectedStatusMax, int64validator.Between(100, 599)),
		"regions": schema.SetAttribute{
			MarkdownDescription: descRegions,
			ElementType:         types.StringType,
			Optional:            true,
			Computed:            true,
			Validators:          []validator.Set{setvalidator.SizeBetween(1, 6)},
			PlanModifiers:       []planmodifier.Set{setplanmodifier.UseNonNullStateForUnknown()},
		},
		"follow_redirects":                  optionalBool(descFollow+" Defaults to `true`.", defaultFollowRedirects),
		"max_redirects":                     optionalInt64(descMaxRedirects+" Defaults to `3`.", defaultMaxRedirects, int64validator.Between(0, 3)),
		"block_http_downgrade":              optionalBool(descBlockDowngrade+" Defaults to `true`.", defaultBlockHTTPDowngrade),
		"fail_after_consecutive":            optionalInt64(descFailAfter+" Defaults to `3`.", defaultFailAfterConsecutive, int64validator.Between(1, 10)),
		"resolve_after_consecutive_success": optionalInt64(descResolveAfter+" Defaults to `2`.", defaultResolveAfterConsecutiveOK, int64validator.Between(1, 10)),
		"latency_warn_ms": schema.Int64Attribute{
			MarkdownDescription: descLatencyWarn,
			Optional:            true,
			Validators:          []validator.Int64{int64validator.AtLeast(1)},
		},
		"ssl_expiry_warn_days": optionalInt64(descSSLExpiry+" Defaults to `14`.", defaultSSLExpiryWarnDays, int64validator.Between(1, 365)),
		"body_contains": schema.StringAttribute{
			MarkdownDescription: descBodyContains,
			Optional:            true,
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 1000)},
		},
		"body_not_contains": schema.StringAttribute{
			MarkdownDescription: descBodyNot,
			Optional:            true,
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 1000)},
		},
		"ip_family": schema.StringAttribute{
			MarkdownDescription: descIPFamily,
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseNonNullStateForUnknown()},
		},
		"skip_tls_verification": schema.BoolAttribute{
			MarkdownDescription: descSkipTLS,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseNonNullStateForUnknown()},
		},
		"max_response_bytes": schema.Int64Attribute{
			MarkdownDescription: descMaxResponse,
			Computed:            true,
			PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseNonNullStateForUnknown()},
		},
	}
}

// ---- data source ----

func checkDataSourceSchema() dsschema.Schema {
	computedString := func(desc string) dsschema.StringAttribute {
		return dsschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	computedInt := func(desc string) dsschema.Int64Attribute {
		return dsschema.Int64Attribute{MarkdownDescription: desc, Computed: true}
	}
	computedBool := func(desc string) dsschema.BoolAttribute {
		return dsschema.BoolAttribute{MarkdownDescription: desc, Computed: true}
	}
	return dsschema.Schema{
		MarkdownDescription: "Look up an Ionhour check by `id` or `slug` (exactly one).",
		Attributes: map[string]dsschema.Attribute{
			"id": dsschema.Int64Attribute{
				MarkdownDescription: "Numeric check id. Set exactly one of `id` and `slug`.",
				Optional:            true,
				Computed:            true,
			},
			"slug": dsschema.StringAttribute{
				MarkdownDescription: "Check slug. Set exactly one of `id` and `slug`.",
				Optional:            true,
				Computed:            true,
				Validators:          slugValidators(),
			},
			"name":               computedString("Check name."),
			"project_id":         computedInt(descProjectID),
			"check_type":         computedString(descCheckType),
			"interval_seconds":   computedInt(descCheckInterval),
			"grace_seconds":      computedInt(descCheckGrace),
			"mute_notifications": computedBool(descMute),
			"down_severity":      computedString(descDownSeverity),
			"late_severity":      computedString(descLateSeverity),
			"status":             computedString(descStatus),
			"paused":             computedBool(descPaused),
			"created_at":         computedString(descCreatedAt),
			"updated_at":         computedString(descUpdatedAt),
			"config": dsschema.SingleNestedAttribute{
				MarkdownDescription: "Probe configuration; null for a check without a live probe config. HTTP-only keys are null for other check types.",
				Computed:            true,
				Attributes: map[string]dsschema.Attribute{
					"url":    computedString("Target URL."),
					"method": computedString(descMethod),
					"custom_headers": dsschema.MapAttribute{
						MarkdownDescription: "Request headers (names lowercased). Sensitive.",
						ElementType:         types.StringType,
						Computed:            true,
						Sensitive:           true,
					},
					"timeout_ms":          computedInt(descTimeout),
					"expected_status_min": computedInt(descStatusMin),
					"expected_status_max": computedInt(descStatusMax),
					"regions": dsschema.SetAttribute{
						MarkdownDescription: "Probe region ids.",
						ElementType:         types.StringType,
						Computed:            true,
					},
					"follow_redirects":                  computedBool(descFollow),
					"max_redirects":                     computedInt(descMaxRedirects),
					"block_http_downgrade":              computedBool(descBlockDowngrade),
					"fail_after_consecutive":            computedInt(descFailAfter),
					"resolve_after_consecutive_success": computedInt(descResolveAfter),
					"latency_warn_ms":                   computedInt(descLatencyWarn),
					"ssl_expiry_warn_days":              computedInt(descSSLExpiry),
					"body_contains":                     computedString(descBodyContains),
					"body_not_contains":                 computedString(descBodyNot),
					"ip_family":                         computedString(descIPFamily),
					"skip_tls_verification":             computedBool(descSkipTLS),
					"max_response_bytes":                computedInt(descMaxResponse),
				},
			},
		},
	}
}
