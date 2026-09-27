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
	_ datasource.DataSource              = (*sitesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*sitesDataSource)(nil)
)

type sitesDataSource struct {
	client *Client
}

func NewSitesDataSource() datasource.DataSource { return &sitesDataSource{} }

type siteModel struct {
	SiteID    types.String `tfsdk:"site_id"`
	Name      types.String `tfsdk:"name"`
	Available types.Bool   `tfsdk:"available"`
}

type sitesModel struct {
	Sites []siteModel `tfsdk:"sites"`
}

func (d *sitesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sites"
}

func (d *sitesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Compute placement sites for cloud and GPU VMs. For VPC and reserved-IP availability, use ibee_network_sites.",
		Attributes: map[string]schema.Attribute{
			"sites": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"site_id":   schema.StringAttribute{Computed: true},
						"name":      schema.StringAttribute{Computed: true},
						"available": schema.BoolAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *sitesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *sitesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out struct {
		Sites *[]struct {
			SiteID    string `json:"site_id"`
			Name      string `json:"name"`
			Available *bool  `json:"available"`
		} `json:"sites"`
	}
	if err := d.client.do(ctx, http.MethodGet, "/compute/sites", nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to list sites", err.Error())
		return
	}

	if out.Sites == nil {
		resp.Diagnostics.AddError("Invalid site catalog", "API omitted sites array.")
		return
	}
	state := sitesModel{Sites: make([]siteModel, 0, len(*out.Sites))}
	for _, s := range *out.Sites {
		m := siteModel{
			SiteID: types.StringValue(s.SiteID),
			Name:   types.StringValue(s.Name),
		}
		if s.Available != nil {
			m.Available = types.BoolValue(*s.Available)
		} else {
			m.Available = types.BoolNull()
		}
		state.Sites = append(state.Sites, m)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
