package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/matiadata/terraform-provider-matia/internal/provider/client"
)

const (
	defaultAPIURL = "https://api.matia.io/v1"

	envAPIToken = "MATIA_API_TOKEN"
	envAPIURL   = "MATIA_API_URL"
)

var _ provider.Provider = &MatiaProvider{}

type MatiaProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &MatiaProvider{version: version}
	}
}

func (p *MatiaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "matia"
	resp.Version = p.version
}

type providerConfigModel struct {
	APIToken types.String `tfsdk:"api_token"`
	APIURL   types.String `tfsdk:"api_url"`
}

func (p *MatiaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A simple Terraform provider for managing matia.",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				Description: "Matia API key, sent as the X-Api-Key header. May also be set with the MATIA_API_TOKEN environment variable. An empty value is treated as unset, so the environment variable still applies.",
				Optional:    true,
				Sensitive:   true,
			},
			"api_url": schema.StringAttribute{
				Description: "Base URL for the Matia v1 API (path must end with /v1). May also be set with the MATIA_API_URL environment variable. Defaults to https://api.matia.io/v1. For local backend use http://localhost:<port>/v1.",
				Optional:    true,
			},
		},
	}
}

func (p *MatiaProvider) Configure(
	ctx context.Context,
	req provider.ConfigureRequest,
	resp *provider.ConfigureResponse,
) {
	var config providerConfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiToken, apiURL, diags := resolveProviderConfig(config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiClient := client.NewMatiaClient(apiURL, apiToken)
	resp.ResourceData = apiClient
	resp.DataSourceData = apiClient
}

// resolveProviderConfig resolves the API token and URL from the provider block,
// falling back to environment variables. Explicit, non-empty configuration always
// takes precedence over the environment.
func resolveProviderConfig(config providerConfigModel) (string, string, diag.Diagnostics) {
	var diags diag.Diagnostics

	if config.APIToken.IsUnknown() {
		diags.AddError(
			"Unknown Matia API token",
			"Cannot configure the Matia provider with an unknown value for api_token. "+
				"Set it to a known value in the provider block, or use the "+envAPIToken+" environment variable.",
		)
	}
	if config.APIURL.IsUnknown() {
		diags.AddError(
			"Unknown Matia API URL",
			"Cannot configure the Matia provider with an unknown value for api_url. "+
				"Set it to a known value in the provider block, or use the "+envAPIURL+" environment variable.",
		)
	}
	if diags.HasError() {
		return "", "", diags
	}

	apiToken := os.Getenv(envAPIToken)
	if v := config.APIToken.ValueString(); v != "" {
		apiToken = v
	}

	apiURL := os.Getenv(envAPIURL)
	if v := config.APIURL.ValueString(); v != "" {
		apiURL = v
	}

	if apiToken == "" {
		diags.AddError(
			"Missing Matia API token",
			"The Matia provider requires an API token. Set the api_token attribute in the "+
				"provider block, or export the "+envAPIToken+" environment variable.",
		)
		return "", "", diags
	}

	if apiURL == "" {
		apiURL = defaultAPIURL
	}

	return apiToken, apiURL, diags
}

func (p *MatiaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewHybridDeploymentAgentResource,
		NewSourceResource,
		NewDestinationResource,
		NewMultiPurposeAssetResource,
		NewIntegrationResource,
		NewIntegrationScheduleResource,
		NewIntegrationSchemaResource,
	}
}

func (p *MatiaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
