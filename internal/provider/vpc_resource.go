package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*vpcResource)(nil)
	_ resource.ResourceWithConfigure = (*vpcResource)(nil)
)

type vpcResource struct {
	client *Client
}

func NewVpcResource() resource.Resource { return &vpcResource{} }

type vpcResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	SiteID               types.String `tfsdk:"site_id"`
	Description          types.String `tfsdk:"description"`
	Cidr                 types.String `tfsdk:"cidr"`
	AutoCidr             types.Bool   `tfsdk:"auto_cidr"`
	CreateDefaultSubnet  types.Bool   `tfsdk:"create_default_subnet"`
	DefaultSubnetID      types.String `tfsdk:"default_subnet_id"`
	OwnedDefaultSubnetID types.String `tfsdk:"owned_default_subnet_id"`
	DefaultSubnetCidr    types.String `tfsdk:"default_subnet_cidr"`
	Status               types.String `tfsdk:"status"`
}

// vpcAPI mirrors the relevant fields of the public API's Vpc object.
type vpcAPI struct {
	VpcID       string      `json:"vpc_id"`
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	SiteID      string      `json:"site_id"`
	Description *string     `json:"description"`
	Cidr        string      `json:"cidr"`
	Status      string      `json:"status"`
	Subnets     []subnetAPI `json:"subnets"`
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
	resp.Schema = schema.Schema{
		Description: "An IBEE VPC — an isolated Layer 3 network in a workspace. " +
			"Deletion removes only the default subnet created by this resource. Other subnets must be managed separately. Import does not adopt child resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true},
			"site_id": schema.StringAttribute{
				Required:      true,
				Description:   "Network placement site (site_id from the ibee_network_sites data source).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(""),
			},
			"cidr": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "RFC1918 IPv4 CIDR (/22–/28). Leave unset to auto-assign.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"auto_cidr": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(true),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"create_default_subnet": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(true),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"default_subnet_cidr": schema.StringAttribute{Optional: true, Description: "Optional RFC1918 CIDR for the default subnet. Requires create_default_subnet=true.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"default_subnet_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Default subnet created with this VPC; never adopts an arbitrary existing subnet.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"owned_default_subnet_id": schema.StringAttribute{Computed: true, Description: "Subnet whose deletion is owned by this resource; empty for imports.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"status": schema.StringAttribute{
				Computed: true,
			},
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

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"name":                  plan.Name.ValueString(),
		"site_id":               plan.SiteID.ValueString(),
		"auto_cidr":             plan.AutoCidr.ValueBool(),
		"create_default_subnet": plan.CreateDefaultSubnet.ValueBool(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.Cidr.IsNull() && !plan.Cidr.IsUnknown() {
		body["cidr"] = plan.Cidr.ValueString()
		body["auto_cidr"] = false
	}

	if !plan.DefaultSubnetCidr.IsNull() && !plan.DefaultSubnetCidr.IsUnknown() {
		if !plan.CreateDefaultSubnet.ValueBool() {
			resp.Diagnostics.AddError("Invalid VPC configuration", "default_subnet_cidr requires create_default_subnet=true")
			return
		}
		body["default_subnet_cidr"] = plan.DefaultSubnetCidr.ValueString()
	}
	var out vpcAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create VPC", err.Error())
		return
	}

	if out.identifier() == "" {
		resp.Diagnostics.AddError("Invalid VPC response", "API omitted the VPC ID; inspect the portal before retrying.")
		return
	}
	plan.ID = types.StringValue(out.identifier())
	if out.Cidr != "" {
		plan.Cidr = types.StringValue(out.Cidr)
	} else if plan.Cidr.IsUnknown() {
		plan.Cidr = types.StringNull()
	}
	plan.Status = types.StringValue(out.Status)
	if out.Description != nil {
		plan.Description = types.StringValue(*out.Description)
	} else {
		plan.Description = types.StringValue("")
	}
	plan.DefaultSubnetID = types.StringNull()
	plan.OwnedDefaultSubnetID = types.StringNull()
	if plan.CreateDefaultSubnet.ValueBool() {
		if len(out.Subnets) == 1 && out.Subnets[0].identifier() != "" {
			plan.DefaultSubnetID = types.StringValue(out.Subnets[0].identifier())
			plan.OwnedDefaultSubnetID = plan.DefaultSubnetID
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if out.Cidr == "" {
		resp.Diagnostics.AddError("Invalid VPC create response", "The VPC ID was saved, but the API omitted its CIDR.")
		return
	}
	if plan.CreateDefaultSubnet.ValueBool() && plan.OwnedDefaultSubnetID.IsNull() {
		resp.Diagnostics.AddError("Default subnet ownership could not be established", "The VPC ID was saved. The create response did not identify exactly one default subnet; import/manage the subnet separately before deletion.")
		return
	}
	if err := waitNetworkStatus(ctx, r.client, func(ctx context.Context) (string, error) {
		var current vpcAPI
		err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+url.PathEscape(plan.ID.ValueString()), nil, &current)
		if err != nil {
			return "", err
		}
		if current.identifier() != plan.ID.ValueString() {
			return "", fmt.Errorf("readiness response returned another VPC ID")
		}
		plan.Status = types.StringValue(current.Status)
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
	err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+url.PathEscape(state.ID.ValueString()), nil, &out)
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read VPC", err.Error())
		return
	}

	state.Name = types.StringValue(out.Name)
	if out.SiteID != "" {
		state.SiteID = types.StringValue(out.SiteID)
	}
	state.Cidr = types.StringValue(out.Cidr)
	state.Status = types.StringValue(out.Status)
	if out.Description != nil {
		state.Description = types.StringValue(*out.Description)
	}
	if out.identifier() != state.ID.ValueString() || out.Name == "" || out.SiteID == "" || out.Cidr == "" {
		resp.Diagnostics.AddError("Invalid VPC response", "API omitted required VPC fields")
		return
	}
	if out.Description == nil {
		state.Description = types.StringValue("")
	}
	// Preserve creation-time ownership. Refresh never adopts a subnet.
	if !state.OwnedDefaultSubnetID.IsNull() {
		if out.Subnets == nil {
			resp.Diagnostics.AddError("Invalid VPC response", "API omitted subnets; preserving recorded default subnet ownership.")
			return
		}
		found := false
		for _, subnet := range out.Subnets {
			if subnet.identifier() == state.OwnedDefaultSubnetID.ValueString() {
				if !state.DefaultSubnetCidr.IsNull() {
					state.DefaultSubnetCidr = types.StringValue(subnet.Cidr)
				}
				found = true
			}
		}
		if !found {
			state.OwnedDefaultSubnetID = types.StringNull()
			state.DefaultSubnetID = types.StringNull()
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"name": plan.Name.ValueString(), "description": plan.Description.ValueString()}
	var out vpcAPI
	if err := r.client.do(ctx, http.MethodPatch, "/networking/vpcs/"+url.PathEscape(plan.ID.ValueString()), body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to update VPC", err.Error())
		return
	}
	if out.identifier() != plan.ID.ValueString() {
		resp.Diagnostics.AddError("Invalid VPC update response", "API omitted the VPC ID")
		return
	}
	plan.Status = types.StringValue(out.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpcID := state.ID.ValueString()

	// The service currently cascades subnet deletion even though the published
	// DELETE contract says the VPC must be empty. Inspect children before any
	// mutation to avoid indirectly destroying an unmanaged subnet.
	var children []subnetAPI
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+url.PathEscape(vpcID)+"/subnets", nil, &children); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Cannot verify VPC subnet ownership", err.Error())
		return
	}
	if children == nil {
		resp.Diagnostics.AddError("Cannot verify VPC subnet ownership", "The API did not return a subnet array; no resources were deleted.")
		return
	}
	for _, child := range children {
		if child.identifier() == "" {
			resp.Diagnostics.AddError("Cannot verify VPC subnet ownership", "The API returned a subnet without an ID; no resources were deleted.")
			return
		}
		if child.identifier() != state.OwnedDefaultSubnetID.ValueString() {
			resp.Diagnostics.AddError("VPC contains separately managed subnets", "Remove or import and destroy subnet "+child.identifier()+" before deleting the VPC. The service would otherwise cascade its deletion.")
			return
		}
	}
	// Only the subnet explicitly returned by this resource's Create is owned.
	// Imported and independently created subnets are never removed here.
	if sid := state.OwnedDefaultSubnetID.ValueString(); sid != "" {
		if err := r.client.do(ctx, http.MethodDelete, "/networking/vpcs/"+url.PathEscape(vpcID)+"/subnets/"+url.PathEscape(sid), nil, nil); err != nil && !IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to delete owned default subnet", err.Error())
			return
		}
	}

	if err := r.client.do(ctx, http.MethodDelete, "/networking/vpcs/"+url.PathEscape(vpcID), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete VPC", err.Error())
		return
	}
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("auto_cidr"), true)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("create_default_subnet"), false)...)
}
