// Package provider implements the Ionhour Terraform provider on the Terraform
// Plugin Framework, on top of the generated Integration API v1 client
// (github.com/ionhour/go-sdk/api).
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

const (
	// envAPIKey and envBaseURL mirror the Go client's environment variables.
	envAPIKey  = api.EnvAPIKey
	envBaseURL = api.EnvBaseURL

	// maxAttempts is the client's total attempts per request: a 429 waits its
	// Retry-After once, and a 5xx / network failure is retried once (POST only
	// when it carries an Idempotency-Key, which every create here does).
	// Further 429s are ridden out by apiClient.call within its budget.
	maxAttempts = 2
)

var _ provider.Provider = (*ionhourProvider)(nil)

// New returns the provider factory for the given release version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ionhourProvider{version: version}
	}
}

type ionhourProvider struct {
	version string
}

type providerModel struct {
	APIKey  types.String `tfsdk:"api_key"`
	BaseURL types.String `tfsdk:"base_url"`
}

func (p *ionhourProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ionhour"
	resp.Version = p.version
}

func (p *ionhourProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage [Ionhour](https://ionhour.com) projects, HTTP checks and heartbeat jobs " +
			"through the Integration API v1.\n\n" +
			"Authenticate with an API key. For automation, use a workspace-owned **SERVICE** key with " +
			"`serviceRole` `MEMBER` (deletes need `ADMIN`) covering `mcp:projects`, `mcp:checks` and " +
			"`mcp:jobs`. The provider calls `GET /v1/whoami` when it is configured, fails early on an " +
			"invalid or inactive key, and logs the workspace it acts on at INFO (`TF_LOG=INFO`).\n\n" +
			"~> **State holds secrets.** `ionhour_job.ping_token` and `ping_url` let anyone send OK " +
			"heartbeats for the job, and `ionhour_check.config.custom_headers` often carries credentials. " +
			"Terraform state stores them in plain text: use an encrypted remote backend.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Ionhour API key (`ionh_...`) sent as `Authorization: Bearer`. " +
					"Falls back to the `" + envAPIKey + "` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"base_url": schema.StringAttribute{
				MarkdownDescription: "Integration API base URL including the `/api` prefix. Falls back to `" +
					envBaseURL + "`, then `" + api.DefaultBaseURL + "`. Override it for SIT/UAT.",
				Optional: true,
			},
		},
	}
}

func (p *ionhourProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown Ionhour API key",
			"api_key depends on a value that is only known after apply. Set it from a variable or the "+
				envAPIKey+" environment variable instead.")
	}
	if cfg.BaseURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Unknown Ionhour base URL",
			"base_url depends on a value that is only known after apply. Set it from a variable or the "+
				envBaseURL+" environment variable instead.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	key := firstNonEmpty(strings.TrimSpace(cfg.APIKey.ValueString()), strings.TrimSpace(os.Getenv(envAPIKey)))
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Ionhour API key",
			"Set api_key in the provider block or the "+envAPIKey+" environment variable.")
		return
	}
	baseURL := firstNonEmpty(strings.TrimSpace(cfg.BaseURL.ValueString()), strings.TrimSpace(os.Getenv(envBaseURL)))

	c, err := api.NewClient(
		api.WithAPIKey(key),
		api.WithBaseURL(baseURL),
		api.WithUserAgent(userAgent(p.version, req.TerraformVersion)),
		api.WithMaxAttempts(maxAttempts),
	)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Ionhour provider configuration", err.Error())
		return
	}

	data := newAPIClient(c)
	identity, err := data.whoami(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "validate the Ionhour API key", err)
		return
	}
	// Success is logged, not surfaced as a diagnostic: a warning would print on
	// every plan and apply. Only a failed whoami raises a diagnostic (above).
	tflog.Info(ctx, "authenticated to Ionhour workspace", map[string]any{
		"workspace_id":     identity.WorkspaceId,
		"workspace_name":   identity.WorkspaceName,
		"principal_kind":   string(identity.PrincipalKind),
		"projected_role":   nullableOr(identity.ProjectedRole, "none"),
		"permission_level": string(identity.PermissionLevel),
		"api_key_prefix":   nullableOr(identity.ApiKeyPrefix, "none"),
		"base_url":         c.BaseURL(),
	})

	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *ionhourProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewProjectResource,
		NewCheckResource,
		NewJobResource,
	}
}

func (p *ionhourProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewRegionsDataSource,
		NewProjectDataSource,
		NewCheckDataSource,
		NewJobDataSource,
	}
}

func userAgent(version, terraformVersion string) string {
	ua := "terraform-provider-ionhour/" + version
	if terraformVersion != "" {
		ua += " terraform/" + terraformVersion
	}
	return ua
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// clientFrom extracts the configured client from ProviderData. It returns
// nil without a diagnostic while the provider is not configured yet (the
// framework calls Configure on resources before the provider during
// validation).
func clientFrom(data any, add func(summary, detail string)) *apiClient {
	if data == nil {
		return nil
	}
	c, ok := data.(*apiClient)
	if !ok {
		add("Unexpected provider data", fmt.Sprintf("expected *apiClient, got %T; report this to Ionhour", data))
		return nil
	}
	return c
}

// nullableOr renders a nullable string-like value for logs, or fallback when
// it is null or absent.
func nullableOr[T ~string](n nullable.Nullable[T], fallback string) string {
	if !n.IsSpecified() || n.IsNull() {
		return fallback
	}
	return string(n.MustGet())
}
