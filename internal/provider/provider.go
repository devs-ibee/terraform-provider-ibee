package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*ibeeProvider)(nil)

type ibeeProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ibeeProvider{version: version}
	}
}

type ibeeProviderModel struct {
	Endpoint    types.String `tfsdk:"endpoint"`
	Token       types.String `tfsdk:"token"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
}

func (p *ibeeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ibee"
	resp.Version = p.version
}

func (p *ibeeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage IBEE cloud resources through the IBEE public API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "IBEE API base URL. Defaults to https://api.ibee.ai/v1 (or IBEE_ENDPOINT).",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "IBEE API token (ibee_prod_key_… / ibee_dev_key_…). Falls back to IBEE_TOKEN.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:    true,
				Description: "Numeric workspace ID every request is scoped to. Falls back to IBEE_WORKSPACE_ID.",
			},
		},
	}
}

func (p *ibeeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg ibeeProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := cfg.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv("IBEE_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = "https://api.ibee.ai/v1"
	}

	token := cfg.Token.ValueString()
	if token == "" {
		token = os.Getenv("IBEE_TOKEN")
	}
	if token == "" {
		resp.Diagnostics.AddError(
			"Missing IBEE API token",
			"Set the provider `token` attribute or the IBEE_TOKEN environment variable.",
		)
	}

	ws := cfg.WorkspaceID.ValueString()
	if ws == "" {
		ws = os.Getenv("IBEE_WORKSPACE_ID")
	}
	if ws == "" {
		resp.Diagnostics.AddError(
			"Missing workspace_id",
			"Set the provider `workspace_id` attribute or the IBEE_WORKSPACE_ID environment variable.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	client := NewClient(endpoint, token, ws)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *ibeeProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVpcResource,
		NewVpcSubnetResource,
		NewVpcNodeAttachmentResource,
		NewFirewallGroupResource,
		NewFirewallRuleResource,
		NewCloudVmResource,
	}
}

func (p *ibeeProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSitesDataSource,
		NewComputePlansDataSource,
		NewImagesDataSource,
	}
}
