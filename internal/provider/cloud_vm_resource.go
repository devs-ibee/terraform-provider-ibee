package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*cloudVmResource)(nil)
	_ resource.ResourceWithConfigure = (*cloudVmResource)(nil)
)

type cloudVmResource struct {
	client *Client
}

func NewCloudVmResource() resource.Resource { return &cloudVmResource{} }

type cloudVmModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	SiteID         types.String `tfsdk:"site_id"`
	PlanID         types.String `tfsdk:"plan_id"`
	TemplateID     types.String `tfsdk:"template_id"`
	OsDistro       types.String `tfsdk:"os_distro"`
	OsType         types.String `tfsdk:"os_type"`
	Cpu            types.Int64  `tfsdk:"cpu"`
	RamMb          types.Int64  `tfsdk:"ram_mb"`
	DiskGb         types.Int64  `tfsdk:"disk_gb"`
	Status         types.String `tfsdk:"status"`
	PublicIP       types.String `tfsdk:"public_ip"`
	PublicIPAction types.String `tfsdk:"delete_public_ip_action"`
}

type cloudVmAPI struct {
	VmID     string `json:"vm_id"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	PublicIP string `json:"public_ip"`
}

func (v *cloudVmAPI) identifier() string {
	if v.VmID != "" {
		return v.VmID
	}
	return v.ID
}

func (r *cloudVmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_vm"
}

func (r *cloudVmResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "An IBEE cloud VM. The provider resolves the plan's billing catalog, " +
			"waits for the create operation to finish, and releases the auto-assigned " +
			"public IP on delete (configurable via delete_public_ip_action).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name":        schema.StringAttribute{Required: true, PlanModifiers: replace},
			"site_id":     schema.StringAttribute{Required: true, PlanModifiers: replace},
			"plan_id":     schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Compute plan (see the ibee_compute_plans data source). CPU/RAM/disk and billing follow the plan."},
			"template_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "OS image template (see the ibee_images data source)."},
			"os_distro":   schema.StringAttribute{Required: true, PlanModifiers: replace},
			"os_type": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("linux"),
				PlanModifiers: replace,
			},
			"cpu":     schema.Int64Attribute{Computed: true},
			"ram_mb":  schema.Int64Attribute{Computed: true},
			"disk_gb": schema.Int64Attribute{Computed: true},
			"status":  schema.StringAttribute{Computed: true},
			"public_ip": schema.StringAttribute{
				Computed: true,
			},
			"delete_public_ip_action": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("release"),
				Description: "What happens to an auto-assigned public IP on delete: release or reserve.",
			},
		},
	}
}

func (r *cloudVmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *cloudVmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan cloudVmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve the plan: the create endpoint requires the plan's billing_catalog
	// (the portal frontend copies it off the plans list the same way).
	p, err := r.client.findPlan(ctx, "cloud", plan.SiteID.ValueString(), plan.PlanID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve compute plan", err.Error())
		return
	}
	if len(p.BillingCatalog) == 0 {
		resp.Diagnostics.AddError("Plan has no billing catalog",
			fmt.Sprintf("plan %s carries no billing_catalog; it cannot be used for VM creation", p.PlanID))
		return
	}

	diskGb := p.DiskGb
	if diskGb == 0 {
		diskGb = 50
	}
	body := map[string]any{
		"name":            plan.Name.ValueString(),
		"site_id":         plan.SiteID.ValueString(),
		"os_distro":       plan.OsDistro.ValueString(),
		"os_type":         plan.OsType.ValueString(),
		"template_id":     plan.TemplateID.ValueString(),
		"cpu":             p.Cpu,
		"ram_mb":          p.RamMb,
		"disk_gb":         diskGb,
		"plan_id":         p.PlanID,
		"billing_catalog": p.BillingCatalog,
	}

	var accepted operationAccepted
	err = r.client.doH(ctx, http.MethodPost, "/compute/cloud-vms",
		map[string]string{"X-Idempotency-Key": idempotencyKey()}, body, &accepted)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create cloud VM", err.Error())
		return
	}

	plan.ID = types.StringValue(accepted.VmID)
	plan.Cpu = types.Int64Value(p.Cpu)
	plan.RamMb = types.Int64Value(p.RamMb)
	plan.DiskGb = types.Int64Value(diskGb)

	// Persist the ID immediately so a failed wait still tracks the VM.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	if accepted.OperationID != "" {
		if err := r.client.waitOperation(ctx, accepted.OperationID, 10*time.Minute); err != nil {
			resp.Diagnostics.AddError("Cloud VM create did not complete", err.Error())
			return
		}
	}

	var vm cloudVmAPI
	if err := r.client.do(ctx, http.MethodGet, "/compute/cloud-vms/"+accepted.VmID, nil, &vm); err == nil {
		plan.Status = types.StringValue(vm.Status)
		plan.PublicIP = types.StringValue(vm.PublicIP)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cloudVmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state cloudVmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var vm cloudVmAPI
	err := r.client.do(ctx, http.MethodGet, "/compute/cloud-vms/"+state.ID.ValueString(), nil, &vm)
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read cloud VM", err.Error())
		return
	}
	if vm.Name != "" {
		state.Name = types.StringValue(vm.Name)
	}
	state.Status = types.StringValue(vm.Status)
	state.PublicIP = types.StringValue(vm.PublicIP)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never invoked: every user-settable attribute forces replacement,
// except delete_public_ip_action which only matters at delete time.
func (r *cloudVmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan cloudVmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cloudVmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state cloudVmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	action := state.PublicIPAction.ValueString()
	if action == "" {
		action = "release"
	}
	// deleteCloudVm requires an idempotency key and honors a JSON body that
	// decides the fate of an auto-assigned public IP.
	var accepted operationAccepted
	err := r.client.doH(ctx, http.MethodDelete, "/compute/cloud-vms/"+state.ID.ValueString(),
		map[string]string{"X-Idempotency-Key": idempotencyKey()},
		map[string]any{"public_ip_action": action}, &accepted)
	if err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete cloud VM", err.Error())
		return
	}
	if accepted.OperationID != "" {
		if err := r.client.waitOperation(ctx, accepted.OperationID, 10*time.Minute); err != nil {
			resp.Diagnostics.AddError("Cloud VM delete did not complete", err.Error())
		}
	}
}
