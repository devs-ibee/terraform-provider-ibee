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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
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
	ID       types.String `tfsdk:"id"`
	VpcID    types.String `tfsdk:"vpc_id"`
	Name     types.String `tfsdk:"name"`
	Cidr     types.String `tfsdk:"cidr"`
	AutoCidr types.Bool   `tfsdk:"auto_cidr"`
}

type subnetAPI struct {
	SubnetID string `json:"subnet_id"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Cidr     string `json:"cidr"`
	Status   string `json:"status"`
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
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
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

	body := map[string]any{
		"name":      plan.Name.ValueString(),
		"auto_cidr": plan.AutoCidr.ValueBool(),
	}
	if !plan.Cidr.IsNull() && !plan.Cidr.IsUnknown() {
		body["cidr"] = plan.Cidr.ValueString()
		body["auto_cidr"] = false
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcSubnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcSubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	err := r.client.do(ctx, http.MethodPatch,
		"/networking/vpcs/"+url.PathEscape(plan.VpcID.ValueString())+"/subnets/"+url.PathEscape(plan.ID.ValueString()), body, nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update subnet", err.Error())
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
