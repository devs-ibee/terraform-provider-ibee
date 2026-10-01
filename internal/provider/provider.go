package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*ibeeProvider)(nil)

type ibeeProvider struct{ version string }

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &ibeeProvider{version: version} }
}

type ibeeProviderModel struct {
	Endpoint         types.String `tfsdk:"endpoint"`
	Token            types.String `tfsdk:"token"`
	WorkspaceID      types.String `tfsdk:"workspace_id"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	RequestTimeout   types.String `tfsdk:"request_timeout"`
	OperationTimeout types.String `tfsdk:"operation_timeout"`
}

func (p *ibeeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ibee"
	resp.Version = p.version
}
func (p *ibeeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Manage IBEE infrastructure with Terraform.", Attributes: map[string]schema.Attribute{
		"endpoint":          schema.StringAttribute{Optional: true, Description: "Public API base URL. Precedence: explicit value, IBEE_ENDPOINT, IBEE_BASE_URL, IBEE_ENV (dev/development uses https://api.ibee.co.in/v1; prod/production or unset uses https://api.ibee.ai/v1). HTTPS is required except for local test servers."},
		"token":             schema.StringAttribute{Optional: true, Sensitive: true, Description: "IBEE API token. Falls back to IBEE_TOKEN. Prefer the environment variable to avoid saving a token in configuration."},
		"workspace_id":      schema.StringAttribute{Optional: true, Description: "Workspace identifier attached to every API request. Falls back to IBEE_WORKSPACE_ID."},
		"organization_id":   schema.StringAttribute{Optional: true, Description: "Optional expected organization for billing decisions. Falls back to IBEE_ORGANIZATION_ID. A mismatched decision fails closed."},
		"request_timeout":   schema.StringAttribute{Optional: true, Description: "Timeout for each HTTP request, as a Go duration (for example 90s). Defaults to 90s. Must be between 1s and 10m."},
		"operation_timeout": schema.StringAttribute{Optional: true, Description: "Timeout for asynchronous provisioning and deletion, as a Go duration. Defaults to 20m. Must be between 1s and 2h."},
	}}
}
func resolvedValue(value types.String, env string) string {
	if !value.IsNull() {
		return strings.TrimSpace(value.ValueString())
	}
	return strings.TrimSpace(os.Getenv(env))
}
func configuredClient(cfg ibeeProviderModel) (*Client, diag.Diagnostics) {
	var diagnostics diag.Diagnostics
	for name, value := range map[string]types.String{"endpoint": cfg.Endpoint, "token": cfg.Token, "workspace_id": cfg.WorkspaceID, "organization_id": cfg.OrganizationID, "request_timeout": cfg.RequestTimeout, "operation_timeout": cfg.OperationTimeout} {
		if value.IsUnknown() {
			diagnostics.AddError("Unknown provider configuration", fmt.Sprintf("Provider attribute %s must be known before IBEE resources can be managed.", name))
		}
	}
	if diagnostics.HasError() {
		return nil, diagnostics
	}
	endpoint := resolvedValue(cfg.Endpoint, "IBEE_ENDPOINT")
	if !cfg.Endpoint.IsNull() && endpoint == "" {
		diagnostics.AddError("Empty IBEE endpoint", "Omit endpoint to use environment defaults, or provide a nonempty HTTPS API URL.")
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("IBEE_BASE_URL"))
	}
	if endpoint == "" {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("IBEE_ENV"))) {
		case "dev", "development":
			endpoint = "https://api.ibee.co.in/v1"
		case "", "prod", "production":
			endpoint = "https://api.ibee.ai/v1"
		default:
			diagnostics.AddError("Invalid IBEE_ENV", "Use dev, development, prod, or production, or set endpoint explicitly.")
		}
	}
	endpoint = strings.TrimRight(endpoint, "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		diagnostics.AddError("Invalid IBEE endpoint", "Use an absolute HTTPS API base URL without credentials, query parameters, or fragments.")
	} else {
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		ip := net.ParseIP(host)
		if u.Scheme == "http" && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			diagnostics.AddError("Insecure IBEE endpoint", "HTTP is supported only for loopback test servers. Use HTTPS for the IBEE API.")
		}
		for _, segment := range strings.Split(u.Path, "/") {
			if segment == "." || segment == ".." {
				diagnostics.AddError("Invalid IBEE endpoint", "The API base URL cannot contain path traversal segments.")
				break
			}
		}
	}
	token := resolvedValue(cfg.Token, "IBEE_TOKEN")
	workspace := resolvedValue(cfg.WorkspaceID, "IBEE_WORKSPACE_ID")
	if token == "" {
		diagnostics.AddError("Missing IBEE API token", "Set token or the IBEE_TOKEN environment variable.")
	}
	if strings.ContainsAny(token, "\r\n") {
		diagnostics.AddError("Invalid IBEE API token", "API tokens cannot contain line breaks.")
	}
	if workspace == "" {
		diagnostics.AddError("Missing workspace_id", "Set workspace_id or the IBEE_WORKSPACE_ID environment variable.")
	}
	host := ""
	if u != nil {
		host = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	}
	if (host == "api.ibee.ai" && strings.HasPrefix(token, "ibee_dev_key_")) || (host == "api.ibee.co.in" && strings.HasPrefix(token, "ibee_prod_key_")) {
		diagnostics.AddError("Token environment mismatch", "The API token environment does not match the configured IBEE endpoint.")
	}
	requestTimeout, operationTimeout := 90*time.Second, 20*time.Minute
	for _, setting := range []struct {
		name   string
		value  types.String
		target *time.Duration
		max    time.Duration
	}{{"request_timeout", cfg.RequestTimeout, &requestTimeout, 10 * time.Minute}, {"operation_timeout", cfg.OperationTimeout, &operationTimeout, 2 * time.Hour}} {
		if setting.value.IsNull() {
			continue
		}
		duration, e := time.ParseDuration(setting.value.ValueString())
		if e != nil || duration < time.Second || duration > setting.max {
			diagnostics.AddError("Invalid timeout", fmt.Sprintf("%s must be a duration between 1s and %s.", setting.name, setting.max))
			continue
		}
		*setting.target = duration
	}
	if diagnostics.HasError() {
		return nil, diagnostics
	}
	client := NewClient(endpoint, token, workspace)
	client.organizationID = resolvedValue(cfg.OrganizationID, "IBEE_ORGANIZATION_ID")
	client.http.Timeout = requestTimeout
	client.operationTimeout = operationTimeout
	return client, diagnostics
}
func (p *ibeeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg ibeeProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	client, diagnostics := configuredClient(cfg)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	client.userAgent = "terraform-provider-ibee/" + p.version + " Terraform/" + req.TerraformVersion
	resp.ResourceData = client
	resp.ActionData = client
	resp.DataSourceData = client
}
func (p *ibeeProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVpcResource, NewVpcSubnetResource, NewVpcNodeAttachmentResource,
		NewFirewallGroupResource, NewFirewallRuleResource, NewCloudVmResource,
		NewBlockVolumeResource,
		NewBlockVolumeAttachmentResource,
		NewBucketCORSResource, NewBucketLifecycleResource, NewBucketNotificationsResource,
		NewCDNDistributionResource, NewCDNOriginResource, NewCDNWebsiteResource, NewCDNDomainResource,
		NewFirewallAttachmentResource, NewReservedIPResource, NewReservedIPAttachmentResource,
		NewNATGatewayResource, NewNATPortForwardingRuleResource, NewL4LoadBalancerResource, NewL7LoadBalancerResource,
		NewGpuVmResource, NewCloudVmSnapshotResource, NewGpuVmSnapshotResource,
		NewCloudVmBackupPolicyResource, NewGpuVmBackupPolicyResource,
		NewCloudVmVolumeAttachmentResource, NewGpuVmVolumeAttachmentResource,
		NewBucketResource, NewBucketRetentionResource, NewS3CredentialResource, NewSecretStoreResource, NewSecretResource,
	}
}
func (p *ibeeProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSitesDataSource, NewNetworkSitesDataSource, NewComputePlansDataSource, NewImagesDataSource, NewBillingEligibilityDataSource,
	}
}

func (p *ibeeProvider) Actions(_ context.Context) []func() action.Action {
	return []func() action.Action{NewCDNPurgeAction, NewCDNVerifyDomainAction,
		NewVmPowerAction,
	}
}
