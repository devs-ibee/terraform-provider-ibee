package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*vpcSubnetResource)(nil)
	_ resource.ResourceWithConfigure = (*vpcSubnetResource)(nil)
)

type vpcSubnetResource struct {
	client *Client
}

func NewVpcSubnetResource() resource.Resource { return &vpcSubnetResource{} }

type vpcSubnetModel struct {
	ID           types.String `tfsdk:"id"`
	VpcID        types.String `tfsdk:"vpc_id"`
	Name         types.String `tfsdk:"name"`
	Cidr         types.String `tfsdk:"cidr"`
	AutoCidr     types.Bool   `tfsdk:"auto_cidr"`
	PrefixLength types.Int64  `tfsdk:"prefix_length"`
	DNS          types.List   `tfsdk:"dns"`
	Gateway      types.String `tfsdk:"gateway"`
}

type subnetAPI struct {
	SubnetID string   `json:"subnet_id"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Cidr     string   `json:"cidr"`
	Status   string   `json:"status"`
	DNS      []string `json:"dns"`
	Gateway  string   `json:"gateway"`
}

func (s *subnetAPI) identifier() string {
	if s.SubnetID != "" {
		return s.SubnetID
	}
	return s.ID
}

func (r *vpcSubnetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_subnet"
}

func (r *vpcSubnetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An additional subnet inside an IBEE VPC.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc_id": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
			},
			"cidr": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"prefix_length": schema.Int64Attribute{Optional: true, Computed: true, Description: "Automatic subnet prefix, /22 through /29; inferred from CIDR when not configured. Do not configure together with cidr.", PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured(), int64planmodifier.UseStateForUnknown()}},
			"dns":           schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{types.StringValue("1.1.1.1"), types.StringValue("8.8.8.8")})), Description: "IPv4 DNS resolver addresses advertised to subnet members; updates in place."},
			"gateway":       schema.StringAttribute{Computed: true, Description: "Gateway IPv4 address returned by the networking service."},
			"auto_cidr": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(true),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *vpcSubnetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vpcSubnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var configuredCIDR types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cidr"), &configuredCIDR)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := subnetRequestBody(ctx, plan, false, !configuredCIDR.IsNull())
	if err != nil {
		resp.Diagnostics.AddError("Invalid subnet configuration", err.Error())
		return
	}

	var out subnetAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs/"+url.PathEscape(plan.VpcID.ValueString())+"/subnets", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create subnet", err.Error())
		return
	}
	if out.identifier() == "" {
		resp.Diagnostics.AddError("Invalid subnet response", "API omitted the subnet ID; inspect the portal before retrying.")
		return
	}
	plan.ID = types.StringValue(out.identifier())
	plan.Gateway = types.StringValue(out.Gateway)
	plan.PrefixLength = types.Int64Null()
	if p, err := netip.ParsePrefix(out.Cidr); err == nil {
		plan.PrefixLength = types.Int64Value(int64(p.Bits()))
	}
	resp.Diagnostics.Append(setSubnetDNS(ctx, &plan, out.DNS)...)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	if out.Cidr != "" {
		plan.Cidr = types.StringValue(out.Cidr)
	} else if plan.Cidr.IsUnknown() {
		plan.Cidr = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if out.Cidr == "" {
		resp.Diagnostics.AddError("Invalid subnet create response", "The subnet ID was saved, but the API omitted its CIDR.")
		return
	}
	if err := waitNetworkStatus(ctx, r.client, func(ctx context.Context) (string, error) {
		var current subnetAPI
		err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+url.PathEscape(plan.VpcID.ValueString())+"/subnets/"+url.PathEscape(plan.ID.ValueString()), nil, &current)
		if err == nil && current.identifier() != plan.ID.ValueString() {
			return "", fmt.Errorf("readiness response returned another subnet ID")
		}
		return current.Status, err
	}); err != nil {
		resp.Diagnostics.AddError("Subnet readiness failed", err.Error())
	}
}

func (r *vpcSubnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var out subnetAPI
	err := r.client.do(ctx, http.MethodGet,
		"/networking/vpcs/"+url.PathEscape(state.VpcID.ValueString())+"/subnets/"+url.PathEscape(state.ID.ValueString()), nil, &out)
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read subnet", err.Error())
		return
	}
	if out.identifier() != state.ID.ValueString() || out.Name == "" || out.Cidr == "" {
		resp.Diagnostics.AddError("Invalid subnet response", "API omitted required subnet fields")
		return
	}
	state.Name = types.StringValue(out.Name)
	state.Cidr = types.StringValue(out.Cidr)
	p, err := netip.ParsePrefix(out.Cidr)
	if err != nil {
		resp.Diagnostics.AddError("Invalid subnet response", "API returned an invalid CIDR")
		return
	}
	state.PrefixLength = types.Int64Value(int64(p.Bits()))
	state.Gateway = types.StringValue(out.Gateway)
	resp.Diagnostics.Append(setSubnetDNS(ctx, &state, out.DNS)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcSubnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := subnetRequestBody(ctx, plan, true, false)
	if err != nil {
		resp.Diagnostics.AddError("Invalid subnet configuration", err.Error())
		return
	}
	var out subnetAPI
	err = r.client.do(ctx, http.MethodPatch, "/networking/vpcs/"+url.PathEscape(plan.VpcID.ValueString())+"/subnets/"+url.PathEscape(plan.ID.ValueString()), body, &out)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update subnet", err.Error())
		return
	}
	if out.identifier() != plan.ID.ValueString() || out.Cidr == "" {
		resp.Diagnostics.AddError("Invalid subnet update response", "API omitted required subnet identity/CIDR fields")
		return
	}
	plan.Cidr = types.StringValue(out.Cidr)
	plan.Gateway = types.StringValue(out.Gateway)
	if p, err := netip.ParsePrefix(out.Cidr); err == nil {
		plan.PrefixLength = types.Int64Value(int64(p.Bits()))
	} else {
		resp.Diagnostics.AddError("Invalid subnet update response", "API returned an invalid CIDR")
		return
	}
	resp.Diagnostics.Append(setSubnetDNS(ctx, &plan, out.DNS)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcSubnetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.do(ctx, http.MethodDelete,
		"/networking/vpcs/"+url.PathEscape(state.VpcID.ValueString())+"/subnets/"+url.PathEscape(state.ID.ValueString()), nil, nil)
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete subnet", err.Error())
	}
}

func (r *vpcSubnetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected vpc_id/subnet_id")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("auto_cidr"), true)...)
}

func subnetRequestBody(ctx context.Context, plan vpcSubnetModel, update, explicitCIDR bool) (map[string]any, error) {
	var dns []string
	if d := plan.DNS.ElementsAs(ctx, &dns, false); d.HasError() {
		return nil, fmt.Errorf("dns must be known IPv4 resolver addresses")
	}
	if len(dns) == 0 {
		return nil, fmt.Errorf("at least one DNS server is required")
	}
	for _, value := range dns {
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("DNS server %q must be IPv4", value)
		}
	}
	body := map[string]any{"name": plan.Name.ValueString(), "dns": dns}
	if update {
		return body, nil
	}
	body["auto_cidr"] = plan.AutoCidr.ValueBool()
	if explicitCIDR {
		body["cidr"] = plan.Cidr.ValueString()
		body["auto_cidr"] = false
	} else if !plan.PrefixLength.IsNull() && !plan.PrefixLength.IsUnknown() {
		prefix := plan.PrefixLength.ValueInt64()
		if prefix < 22 || prefix > 29 {
			return nil, fmt.Errorf("prefix_length must be between 22 and 29")
		}
		body["prefix_length"] = prefix
	}
	return body, nil
}
func setSubnetDNS(ctx context.Context, model *vpcSubnetModel, addresses []string) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if len(addresses) == 0 {
		diagnostics.AddError("Invalid subnet response", "API omitted DNS resolver addresses")
		return diagnostics
	}
	value, d := types.ListValueFrom(ctx, types.StringType, addresses)
	diagnostics.Append(d...)
	model.DNS = value
	return diagnostics
}
func (r *vpcSubnetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var config, plan, state vpcSubnetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.Cidr.IsNull() && !config.PrefixLength.IsNull() {
		resp.Diagnostics.AddError("Invalid subnet allocation configuration", "Configure cidr or prefix_length, not both.")
		return
	}
	if req.State.Raw.IsNull() {
		return
	}
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Cidr.IsNull() && (!plan.PrefixLength.Equal(state.PrefixLength) || !plan.VpcID.Equal(state.VpcID)) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("cidr"), types.StringUnknown())...)
	}
}
