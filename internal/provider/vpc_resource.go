package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*vpcResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcResource)(nil)
	_ resource.ResourceWithImportState = (*vpcResource)(nil)
)

type vpcResource struct {
	client *Client
}

func NewVpcResource() resource.Resource { return &vpcResource{} }

type vpcResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	SiteID              types.String `tfsdk:"site_id"`
	Description         types.String `tfsdk:"description"`
	Cidr                types.String `tfsdk:"cidr"`
	AutoCidr            types.Bool   `tfsdk:"auto_cidr"`
	CreateDefaultSubnet types.Bool   `tfsdk:"create_default_subnet"`
	DefaultSubnetID     types.String `tfsdk:"default_subnet_id"`
	DefaultSubnetCidr   types.String `tfsdk:"default_subnet_cidr"`
	ConnectivityType    types.String `tfsdk:"connectivity_type"`
	DefaultNATGatewayID types.String `tfsdk:"default_nat_gateway_id"`
	Status              types.String `tfsdk:"status"`
}

// vpcAPI mirrors the fields of the public API's Vpc object the resource reads.
type vpcAPI struct {
	VpcID            string             `json:"vpc_id"`
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	SiteID           string             `json:"site_id"`
	Description      *string            `json:"description"`
	Cidr             string             `json:"cidr"`
	Status           string             `json:"status"`
	Subnets          []subnetAPI        `json:"subnets"`
	ConnectivityType *string            `json:"connectivity_type"`
	NATGateways      []vpcNATGatewayAPI `json:"nat_gateways"`
}

type vpcNATGatewayAPI struct {
	ID     string `json:"nat_gateway_id"`
	Status string `json:"status"`
}

func (v *vpcAPI) identifier() string {
	if v.VpcID != "" {
		return v.VpcID
	}
	return v.ID
}

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), createOnlyBool()}
	resp.Schema = schema.Schema{
		Description: "An IBEE VPC. Settings that are not configured are not sent and the API's defaults apply. name and description change in place (PATCH); other settings replace the VPC. Destroy deletes the VPC (DELETE); the API decides what happens to its subnets and NAT gateway. Import using the VPC ID.",
		Attributes: map[string]schema.Attribute{
			"id":                     schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":                   schema.StringAttribute{Required: true},
			"site_id":                schema.StringAttribute{Required: true, Description: "Network placement site (site_id from ibee_network_sites).", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"description":            schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"connectivity_type":      schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "public, private or nat_gateway. nat_gateway creates a NAT gateway with the VPC (default_nat_gateway_id)."},
			"cidr":                   schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepString, Description: "IPv4 CIDR of the VPC. Omit to let the API assign one."},
			"auto_cidr":              schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Sent as auto_cidr with create."},
			"create_default_subnet":  schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: keepBool, Description: "Sent as create_default_subnet with create."},
			"default_subnet_cidr":    schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{createOnlyString()}, Description: "Sent as default_subnet_cidr with create."},
			"default_subnet_id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Subnet the create response returned, if any."},
			"default_nat_gateway_id": schema.StringAttribute{Computed: true, Description: "NAT gateway of the VPC, as the API reports it. Reference it in ibee_nat_port_forwarding_rule."},
			"status":                 schema.StringAttribute{Computed: true},
		},
	}
}

func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func vpcPath(id string) string { return "/networking/vpcs/" + url.PathEscape(id) }

// apply records what the API returned; fields it omits keep their value.
func (out vpcAPI) apply(state *vpcResourceModel) {
	if id := out.identifier(); id != "" {
		state.ID = types.StringValue(id)
	}
	if out.Name != "" {
		state.Name = types.StringValue(out.Name)
	}
	if out.SiteID != "" {
		state.SiteID = types.StringValue(out.SiteID)
	}
	if out.Description != nil {
		state.Description = types.StringValue(*out.Description)
	} else if state.Description.IsUnknown() {
		state.Description = types.StringNull()
	}
	if out.Cidr != "" {
		state.Cidr = types.StringValue(out.Cidr)
	} else if state.Cidr.IsUnknown() {
		state.Cidr = types.StringNull()
	}
	if out.ConnectivityType != nil {
		state.ConnectivityType = types.StringValue(*out.ConnectivityType)
	} else if state.ConnectivityType.IsUnknown() {
		state.ConnectivityType = types.StringNull()
	}
	state.Status = types.StringValue(out.Status)
	state.DefaultNATGatewayID = types.StringNull()
	if len(out.NATGateways) > 0 && out.NATGateways[0].ID != "" {
		state.DefaultNATGatewayID = types.StringValue(out.NATGateways[0].ID)
	}
	for _, value := range []*types.Bool{&state.AutoCidr, &state.CreateDefaultSubnet} {
		if value.IsUnknown() {
			*value = types.BoolNull()
		}
	}
	if state.DefaultSubnetID.IsUnknown() {
		state.DefaultSubnetID = types.StringNull()
	}
}

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "site_id": plan.SiteID.ValueString()}
	for key, value := range map[string]types.String{"description": plan.Description, "cidr": plan.Cidr, "connectivity_type": plan.ConnectivityType, "default_subnet_cidr": plan.DefaultSubnetCidr} {
		if known(value) {
			body[key] = value.ValueString()
		}
	}
	for key, value := range map[string]types.Bool{"auto_cidr": plan.AutoCidr, "create_default_subnet": plan.CreateDefaultSubnet} {
		if known(value) {
			body[key] = value.ValueBool()
		}
	}
	var out vpcAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create VPC", err.Error())
		return
	}
	if out.identifier() == "" {
		resp.Diagnostics.AddError("Invalid VPC response", "The API returned no VPC ID; inspect the portal before retrying.")
		return
	}
	if len(out.Subnets) > 0 && out.Subnets[0].identifier() != "" {
		plan.DefaultSubnetID = types.StringValue(out.Subnets[0].identifier())
	}
	out.apply(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := waitNetworkStatus(ctx, r.client, func(ctx context.Context) (string, error) {
		var current vpcAPI
		if err := r.client.do(ctx, http.MethodGet, vpcPath(plan.ID.ValueString()), nil, &current); err != nil {
			return "", err
		}
		current.apply(&plan)
		return current.Status, nil
	}); err != nil {
		resp.Diagnostics.AddError("VPC readiness failed", err.Error())
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out vpcAPI
	if err := r.client.do(ctx, http.MethodGet, vpcPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read VPC", err.Error())
		return
	}
	out.apply(&state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if known(plan.Description) {
		body["description"] = plan.Description.ValueString()
	}
	var out vpcAPI
	if err := r.client.do(ctx, http.MethodPatch, vpcPath(plan.ID.ValueString()), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update VPC", err.Error())
		return
	}
	if err := r.client.do(ctx, http.MethodGet, vpcPath(plan.ID.ValueString()), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to refresh VPC", err.Error())
		return
	}
	out.apply(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.do(ctx, http.MethodDelete, vpcPath(state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete VPC", err.Error())
	}
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
