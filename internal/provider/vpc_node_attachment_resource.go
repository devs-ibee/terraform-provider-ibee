package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*vpcNodeAttachmentResource)(nil)
	_ resource.ResourceWithConfigure = (*vpcNodeAttachmentResource)(nil)
)

type vpcNodeAttachmentResource struct {
	client *Client
}

func NewVpcNodeAttachmentResource() resource.Resource { return &vpcNodeAttachmentResource{} }

type vpcNodeAttachmentModel struct {
	ID           types.String `tfsdk:"id"`
	VpcID        types.String `tfsdk:"vpc_id"`
	SubnetID     types.String `tfsdk:"subnet_id"`
	VmID         types.String `tfsdk:"vm_id"`
	Connectivity types.String `tfsdk:"connectivity"`
	PrivateIP    types.String `tfsdk:"private_ip"`
	Gateway      types.String `tfsdk:"gateway"`
}

type allocationAPI struct {
	AllocationID string `json:"allocation_id"`
	VmID         string `json:"vm_id"`
	SubnetID     string `json:"subnet_id"`
	PrivateIP    string `json:"private_ip"`
	Gateway      string `json:"gateway"`
	Connectivity string `json:"connectivity"`
}

func (r *vpcNodeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_node_attachment"
}

func (r *vpcNodeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Attaches a VM to a VPC subnet. The platform assigns the next free " +
			"private IP in the subnet CIDR; it is exposed as the private_ip attribute.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc_id":    schema.StringAttribute{Required: true, PlanModifiers: replace},
			"subnet_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"vm_id":     schema.StringAttribute{Required: true, PlanModifiers: replace},
			"connectivity": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("private"),
				Description:   "private, public_ip, or nat.",
				PlanModifiers: replace,
			},
			"private_ip": schema.StringAttribute{Computed: true},
			"gateway":    schema.StringAttribute{Computed: true},
		},
	}
}

func (r *vpcNodeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vpcNodeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcNodeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"vm_id":        plan.VmID.ValueString(),
		"subnet_id":    plan.SubnetID.ValueString(),
		"connectivity": plan.Connectivity.ValueString(),
	}
	var out allocationAPI
	if err := r.client.do(ctx, http.MethodPost, "/networking/vpcs/"+plan.VpcID.ValueString()+"/nodes", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to attach VM to VPC subnet", err.Error())
		return
	}

	if out.AllocationID != "" {
		plan.ID = types.StringValue(out.AllocationID)
	} else {
		plan.ID = types.StringValue(plan.VpcID.ValueString() + "/" + plan.VmID.ValueString())
	}
	plan.PrivateIP = types.StringValue(out.PrivateIP)
	plan.Gateway = types.StringValue(out.Gateway)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcNodeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcNodeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The list endpoint may return a bare array or an object wrapper.
	var raw json.RawMessage
	if err := r.client.do(ctx, http.MethodGet, "/networking/vpcs/"+state.VpcID.ValueString()+"/nodes", nil, &raw); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to list VPC nodes", err.Error())
		return
	}
	var nodes []allocationAPI
	if err := json.Unmarshal(raw, &nodes); err != nil {
		var wrapper struct {
			Nodes []allocationAPI `json:"nodes"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil {
			nodes = wrapper.Nodes
		}
	}

	for _, n := range nodes {
		if n.VmID == state.VmID.ValueString() {
			state.PrivateIP = types.StringValue(n.PrivateIP)
			state.Gateway = types.StringValue(n.Gateway)
			if n.Connectivity != "" {
				state.Connectivity = types.StringValue(n.Connectivity)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	// VM no longer attached.
	resp.State.RemoveResource(ctx)
}

// Update is never invoked: all user-settable attributes force replacement.
func (r *vpcNodeAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcNodeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcNodeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcNodeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.do(ctx, http.MethodDelete,
		"/networking/vpcs/"+state.VpcID.ValueString()+"/nodes/"+state.VmID.ValueString(), nil, nil)
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to detach VM from VPC subnet", err.Error())
	}
}
