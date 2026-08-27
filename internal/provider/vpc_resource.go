package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	SiteID              types.String `tfsdk:"site_id"`
	Description         types.String `tfsdk:"description"`
	Cidr                types.String `tfsdk:"cidr"`
	AutoCidr            types.Bool   `tfsdk:"auto_cidr"`
	CreateDefaultSubnet types.Bool   `tfsdk:"create_default_subnet"`
	DefaultSubnetID     types.String `tfsdk:"default_subnet_id"`
	Status              types.String `tfsdk:"status"`
}

// vpcAPI mirrors the relevant fields of the public API's Vpc object.
type vpcAPI struct {
	VpcID       string  `json:"vpc_id"`
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	SiteID      string  `json:"site_id"`
	Description *string `json:"description"`
	Cidr        string  `json:"cidr"`
	Status      string  `json:"status"`
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
			"Deleting the resource also deletes its subnets (a VPC must be empty before deletion).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"site_id": schema.StringAttribute{
				Required:      true,
				Description:   "Network placement site (site_id from the ibee_sites data source).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
			"cidr": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "RFC1918 IPv4 CIDR (/22–/28). Leave unset to auto-assign.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"auto_cidr": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"create_default_subnet": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"default_subnet_id": schema.StringAttribute{
				Computed:    true,
				Description: "First subnet of the VPC (useful for VM attachments).",
			},
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

	var out vpcAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create VPC", err.Error())
		return
	}

	plan.ID = types.StringValue(out.identifier())
	plan.Cidr = types.StringValue(out.Cidr)
	plan.Status = types.StringValue(out.Status)
	if out.Description != nil {
		plan.Description = types.StringValue(*out.Description)
	} else if plan.Description.IsUnknown() {
		plan.Description = types.StringNull()
	}

	plan.DefaultSubnetID = types.StringNull()
	if plan.CreateDefaultSubnet.ValueBool() {
		if id, err := r.firstSubnetID(ctx, plan.ID.ValueString()); err == nil && id != "" {
			plan.DefaultSubnetID = types.StringValue(id)
		}
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
	err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+state.ID.ValueString(), nil, &out)
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
	if id, err := r.firstSubnetID(ctx, state.ID.ValueString()); err == nil && id != "" {
		state.DefaultSubnetID = types.StringValue(id)
	} else {
		state.DefaultSubnetID = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"description": plan.Description.ValueString()}
	if err := r.client.do(ctx, http.MethodPatch, "/networking/vpcs/"+plan.ID.ValueString(), body, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpcID := state.ID.ValueString()

	// A VPC must be empty before deletion — remove its subnets first.
	ids, err := r.subnetIDs(ctx, vpcID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list VPC subnets before delete", err.Error())
		return
	}
	for _, sid := range ids {
		if err := r.client.do(ctx, http.MethodDelete, "/networking/vpcs/"+vpcID+"/subnets/"+sid, nil, nil); err != nil && !IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to delete VPC subnet "+sid, err.Error())
			return
		}
	}

	if err := r.client.do(ctx, http.MethodDelete, "/networking/vpcs/"+vpcID, nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete VPC", err.Error())
		return
	}
}

// subnetIDs tolerates both response shapes: a bare array or {"subnets": [...]}.
func (r *vpcResource) subnetIDs(ctx context.Context, vpcID string) ([]string, error) {
	var raw any
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+vpcID+"/subnets", nil, &raw); err != nil {
		return nil, err
	}
	items, ok := raw.([]any)
	if !ok {
		if m, isMap := raw.(map[string]any); isMap {
			items, _ = m["subnets"].([]any)
		}
	}
	var ids []string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["subnet_id"].(string); id != "" {
			ids = append(ids, id)
		} else if id, _ := m["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *vpcResource) firstSubnetID(ctx context.Context, vpcID string) (string, error) {
	ids, err := r.subnetIDs(ctx, vpcID)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}
