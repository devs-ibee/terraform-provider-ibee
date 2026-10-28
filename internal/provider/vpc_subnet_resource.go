package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*vpcSubnetResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcSubnetResource)(nil)
	_ resource.ResourceWithImportState = (*vpcSubnetResource)(nil)
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
		Description: "A subnet inside an IBEE VPC. The public API has no subnet update operation, so every change replaces the subnet. Settings that are not configured are not sent and the API's defaults apply. Import as vpc_id/subnet_id.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"vpc_id":        schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"name":          schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"cidr":          schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}, Description: "IPv4 CIDR. Omit to let the API assign one."},
			"auto_cidr":     schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), createOnlyBool()}, Description: "Sent as auto_cidr with create."},
			"prefix_length": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplaceIfConfigured()}, Description: "Prefix length for an automatically assigned CIDR."},
			"dns":           schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown(), listplanmodifier.RequiresReplaceIfConfigured()}, Description: "DNS resolver addresses advertised to subnet members."},
			"gateway":       schema.StringAttribute{Computed: true, Description: "Gateway IPv4 address returned by the API."},
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

func subnetPath(vpcID, subnetID string) string {
	return "/networking/vpcs/" + url.PathEscape(vpcID) + "/subnets/" + url.PathEscape(subnetID)
}

// apply records what the API returned; fields it omits keep their value.
func (out subnetAPI) apply(ctx context.Context, m *vpcSubnetModel) {
	if id := out.identifier(); id != "" {
		m.ID = types.StringValue(id)
	}
	if out.Name != "" {
		m.Name = types.StringValue(out.Name)
	}
	if out.Cidr != "" {
		m.Cidr = types.StringValue(out.Cidr)
		if _, bits, ok := strings.Cut(out.Cidr, "/"); ok {
			var n int64
			if _, err := fmt.Sscan(bits, &n); err == nil {
				m.PrefixLength = types.Int64Value(n)
			}
		}
	}
	if out.DNS != nil {
		m.DNS, _ = types.ListValueFrom(ctx, types.StringType, out.DNS)
	}
	m.Gateway = types.StringValue(out.Gateway)
	if m.Cidr.IsUnknown() {
		m.Cidr = types.StringNull()
	}
	if m.PrefixLength.IsUnknown() {
		m.PrefixLength = types.Int64Null()
	}
	if m.DNS.IsUnknown() {
		m.DNS = types.ListNull(types.StringType)
	}
	if m.AutoCidr.IsUnknown() {
		m.AutoCidr = types.BoolNull()
	}
}

func (r *vpcSubnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if known(plan.Cidr) {
		body["cidr"] = plan.Cidr.ValueString()
	}
	if known(plan.AutoCidr) {
		body["auto_cidr"] = plan.AutoCidr.ValueBool()
	}
	if known(plan.PrefixLength) {
		body["prefix_length"] = plan.PrefixLength.ValueInt64()
	}
	if known(plan.DNS) {
		var dns []string
		resp.Diagnostics.Append(plan.DNS.ElementsAs(ctx, &dns, false)...)
		body["dns"] = dns
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var out subnetAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs/"+url.PathEscape(plan.VpcID.ValueString())+"/subnets", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create subnet", err.Error())
		return
	}
	if out.identifier() == "" {
		resp.Diagnostics.AddError("Invalid subnet response", "The API returned no subnet ID; inspect the portal before retrying.")
		return
	}
	out.apply(ctx, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := waitNetworkStatus(ctx, r.client, func(ctx context.Context) (string, error) {
		var current subnetAPI
		if err := r.client.do(ctx, http.MethodGet, subnetPath(plan.VpcID.ValueString(), plan.ID.ValueString()), nil, &current); err != nil {
			return "", err
		}
		current.apply(ctx, &plan)
		return current.Status, nil
	}); err != nil {
		resp.Diagnostics.AddError("Subnet readiness failed", err.Error())
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcSubnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out subnetAPI
	if err := r.client.do(ctx, http.MethodGet, subnetPath(state.VpcID.ValueString(), state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read subnet", err.Error())
		return
	}
	out.apply(ctx, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update has nothing to send: every subnet setting replaces the subnet.
func (r *vpcSubnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
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
	if err := r.client.do(ctx, http.MethodDelete, subnetPath(state.VpcID.ValueString(), state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
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
}
