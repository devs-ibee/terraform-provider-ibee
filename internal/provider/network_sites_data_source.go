package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*networkSitesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*networkSitesDataSource)(nil)
)

type networkSitesDataSource struct{ client *Client }

func NewNetworkSitesDataSource() datasource.DataSource { return &networkSitesDataSource{} }

type networkSiteModel struct {
	SiteID    types.String `tfsdk:"site_id"`
	Name      types.String `tfsdk:"name"`
	Available types.Bool   `tfsdk:"available"`
	Message   types.String `tfsdk:"message"`
}
type networkSitesModel struct {
	Sites []networkSiteModel `tfsdk:"sites"`
}

func (d *networkSitesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_sites"
}
func (d *networkSitesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Authoritative VPC and reserved-IP site availability from the networking service. Select only entries with available=true. Compute site availability does not imply networking availability.", Attributes: map[string]schema.Attribute{
		"sites": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"site_id":   schema.StringAttribute{Computed: true, Description: "Canonical site ID accepted by networking APIs."},
			"name":      schema.StringAttribute{Computed: true, Description: "Networking site_name returned by the API."},
			"available": schema.BoolAttribute{Computed: true, Description: "Whether this networking service currently supports provisioning in the site."},
			"message":   schema.StringAttribute{Computed: true, Description: "API-provided explanation of site unavailability, when present."},
		}}},
	}}
}
func (d *networkSitesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}
func (d *networkSitesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out []struct {
		SiteID    string  `json:"site_id"`
		Name      string  `json:"site_name"`
		Available *bool   `json:"available"`
		Message   *string `json:"message"`
	}
	if err := d.client.do(ctx, http.MethodGet, "/networking/sites", nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to list networking sites", err.Error())
		return
	}
	if out == nil {
		resp.Diagnostics.AddError("Invalid networking site catalog", "API must return an array of networking sites, including an empty array when none are configured.")
		return
	}
	state := networkSitesModel{Sites: make([]networkSiteModel, 0, len(out))}
	seen := map[string]bool{}
	for _, site := range out {
		if site.SiteID == "" || site.Name == "" || site.Available == nil {
			resp.Diagnostics.AddError("Invalid networking site catalog", "Each site must contain a nonempty site_id and site_name, and an explicit boolean available.")
			return
		}
		if seen[site.SiteID] {
			resp.Diagnostics.AddError("Invalid networking site catalog", "API returned duplicate site_id "+site.SiteID)
			return
		}
		seen[site.SiteID] = true
		state.Sites = append(state.Sites, networkSiteModel{SiteID: types.StringValue(site.SiteID), Name: types.StringValue(site.Name), Available: types.BoolValue(*site.Available), Message: types.StringPointerValue(site.Message)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
