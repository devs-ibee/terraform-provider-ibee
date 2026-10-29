package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = (*containerRegistriesDataSource)(nil)

type containerRegistriesDataSource struct{ client *Client }

func NewContainerRegistriesDataSource() datasource.DataSource {
	return &containerRegistriesDataSource{}
}

type containerRegistrySummaryModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	Visibility              types.String `tfsdk:"visibility"`
	VulnerabilityPullPolicy types.String `tfsdk:"vulnerability_pull_policy"`
	ObservedState           types.String `tfsdk:"observed_state"`
	CreatedAt               types.String `tfsdk:"created_at"`
}

type containerRegistriesModel struct {
	Registries []containerRegistrySummaryModel `tfsdk:"registries"`
}

func (d *containerRegistriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registries"
}

func (d *containerRegistriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The workspace's Container Registry registries, newest first, as the API lists them (every page).",
		Attributes: map[string]schema.Attribute{
			"registries": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":                        schema.StringAttribute{Computed: true, Description: "Registry ID."},
						"name":                      schema.StringAttribute{Computed: true},
						"visibility":                schema.StringAttribute{Computed: true},
						"vulnerability_pull_policy": schema.StringAttribute{Computed: true},
						"observed_state":            schema.StringAttribute{Computed: true},
						"created_at":                schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *containerRegistriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *containerRegistriesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	state := containerRegistriesModel{Registries: []containerRegistrySummaryModel{}}
	// Pages of 100 (the API's maximum) until the API's total is reached or a page is empty.
	for page := 1; ; page++ {
		var out struct {
			Items *[]struct {
				containerRegistryAPI
				CreatedAt string `json:"created_at"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err := d.client.do(ctx, http.MethodGet, fmt.Sprintf("/container-registry/registries?page=%d&page_size=100", page), nil, &out); err != nil {
			resp.Diagnostics.AddError("Failed to list container registries", err.Error())
			return
		}
		if out.Items == nil {
			resp.Diagnostics.AddError("Invalid container registries response", "The response omitted the items array.")
			return
		}
		for _, item := range *out.Items {
			state.Registries = append(state.Registries, containerRegistrySummaryModel{
				ID:                      types.StringValue(item.RegistryID),
				Name:                    types.StringValue(item.RegistryName),
				Visibility:              types.StringValue(item.Visibility),
				VulnerabilityPullPolicy: types.StringValue(item.Security.VulnerabilityPullPolicy),
				ObservedState:           types.StringValue(item.ObservedState),
				CreatedAt:               types.StringValue(item.CreatedAt),
			})
		}
		if len(*out.Items) == 0 || len(state.Registries) >= out.Total {
			break
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
