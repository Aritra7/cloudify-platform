package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultEndpoint = "http://localhost:8080"

type cloudifyProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
}

func New(version string) func() frameworkprovider.Provider {
	return func() frameworkprovider.Provider { return &cloudifyProvider{version: version} }
}

func (provider *cloudifyProvider) Metadata(
	_ context.Context, _ frameworkprovider.MetadataRequest, response *frameworkprovider.MetadataResponse,
) {
	response.TypeName = "cloudify"
	response.Version = provider.version
}

func (provider *cloudifyProvider) Schema(
	_ context.Context, _ frameworkprovider.SchemaRequest, response *frameworkprovider.SchemaResponse,
) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Manage asynchronous application migrations through the Cloudify Go control-plane API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Cloudify API base URL. Defaults to `CLOUDIFY_ENDPOINT` or `http://localhost:8080`.",
			},
			"token": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Bearer token. Defaults to `CLOUDIFY_TOKEN`. The value is never returned by the API.",
			},
		},
	}
}

func (provider *cloudifyProvider) Configure(
	ctx context.Context, request frameworkprovider.ConfigureRequest, response *frameworkprovider.ConfigureResponse,
) {
	var config providerModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}
	if config.Endpoint.IsUnknown() || config.Token.IsUnknown() {
		response.Diagnostics.AddError(
			"Unknown provider configuration",
			"The Cloudify endpoint and token must be known during provider configuration.",
		)
		return
	}
	endpoint := os.Getenv("CLOUDIFY_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	token := os.Getenv("CLOUDIFY_TOKEN")
	if !config.Token.IsNull() {
		token = config.Token.ValueString()
	}
	client, err := NewClient(endpoint, token, fmt.Sprintf("terraform-provider-cloudify/%s", provider.version), nil)
	if err != nil {
		response.Diagnostics.AddError("Invalid Cloudify client configuration", err.Error())
		return
	}
	response.DataSourceData = client
	response.ResourceData = client
}

func (provider *cloudifyProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewMigrationResource}
}

func (provider *cloudifyProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewMigrationDataSource}
}
