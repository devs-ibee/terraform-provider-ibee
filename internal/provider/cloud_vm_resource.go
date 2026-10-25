package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithImportState = (*cloudVmResource)(nil)

type cloudVmResource struct {
	client *Client
	vmType string
}

func NewCloudVmResource() resource.Resource { return &cloudVmResource{vmType: "cloud"} }
func NewGpuVmResource() resource.Resource   { return &cloudVmResource{vmType: "gpu"} }
func (r *cloudVmResource) kind() string {
	if r.vmType == "gpu" {
		return "gpu"
	}
	return "cloud"
}
func (r *cloudVmResource) route(id string) string {
	p := "/compute/" + r.kind() + "-vms"
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}

type cloudVmModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	SiteID          types.String `tfsdk:"site_id"`
	PlanID          types.String `tfsdk:"plan_id"`
	BillingInterval types.String `tfsdk:"billing_interval"`
	TemplateID      types.String `tfsdk:"template_id"`
	OsDistro        types.String `tfsdk:"os_distro"`
	OsType          types.String `tfsdk:"os_type"`
	Cpu             types.Int64  `tfsdk:"cpu"`
	RamMb           types.Int64  `tfsdk:"ram_mb"`
	DiskGb          types.Int64  `tfsdk:"disk_gb"`
	GpuCount        types.Int64  `tfsdk:"gpu_count"`
	GpuModel        types.String `tfsdk:"gpu_model"`
	Status          types.String `tfsdk:"status"`
	PublicIP        types.String `tfsdk:"public_ip"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	PublicIPAction  types.String `tfsdk:"delete_public_ip_action"`
	SSHKeyIDs       types.Set    `tfsdk:"ssh_key_ids"`
	Tags            types.Set    `tfsdk:"tags"`
}

type cloudVmAPI struct {
	VmID           string              `json:"vm_id"`
	ID             string              `json:"id"`
	MongoID        string              `json:"_id"`
	Name           string              `json:"name"`
	Status         string              `json:"status"`
	PublicIP       *string             `json:"public_ip"`
	PrivateIP      *string             `json:"private_ip"`
	SiteID         *string             `json:"site_id"`
	PlanID         *string             `json:"plan_id"`
	TemplateID     *string             `json:"template_id"`
	OsDistro       string              `json:"os_distro"`
	OsType         string              `json:"os_type"`
	Cpu            int64               `json:"cpu"`
	RamMb          int64               `json:"ram_mb"`
	DiskGb         int64               `json:"disk_gb"`
	GpuCount       int64               `json:"gpu_count"`
	GpuModel       *string             `json:"gpu_model"`
	SSHKeyIDs      *[]string           `json:"ssh_key_ids"`
	Tags           *[]string           `json:"tags"`
	DataVolumes    *[]vmDataVolume     `json:"data_volumes"`
	BillingCatalog *computeBillingTerm `json:"billing_catalog"`
}

func (v *cloudVmAPI) identifier() string {
	if v.VmID != "" {
		return v.VmID
	}
	if v.ID != "" {
		return v.ID
	}
	return v.MongoID
}
func (r *cloudVmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind() + "_vm"
}
func (r *cloudVmResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	required := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Required: true, Description: description, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}}
	}
	empty := types.SetValueMust(types.StringType, []attr.Value{})
	osTypes := []string{"linux", "windows"}
	if r.kind() == "gpu" {
		osTypes = []string{"linux"}
	}
	resp.Schema = schema.Schema{Description: "A " + r.kind() + " VM. A plan_id change resizes the VM in place and an ssh_key_ids change updates its access in place; the API decides whether a change is allowed and stops and starts the VM when it must. Other configuration changes replace the VM. Imports use the VM ID.", Attributes: map[string]schema.Attribute{
		"id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name": required("VM name."), "site_id": required("Placement site from ibee_sites."), "template_id": required("Image template from ibee_images."), "os_distro": required("Image OS distribution."),
		"plan_id":          schema.StringAttribute{Required: true, Validators: []validator.String{computeNonEmpty()}, Description: "Plan from ibee_compute_plans. A change resizes the VM in place to the new plan's CPU, memory and disk; the API refuses changes it cannot make, such as shrinking the root disk."},
		"billing_interval": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), billingIntervalReplace()}, Description: "Billing term from the plan's billing options, such as HOURLY or MONTHLY. Omit to send the plan's catalog unchanged and let the API decide. No API operation changes the term, so changing it replaces the VM; deleting a VM does not cancel contractual charges."},
		"os_type":          schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("linux"), PlanModifiers: replace, Validators: []validator.String{computeOneOf(osTypes...)}},
		"cpu":              schema.Int64Attribute{Computed: true}, "ram_mb": schema.Int64Attribute{Computed: true}, "disk_gb": schema.Int64Attribute{Computed: true}, "gpu_count": schema.Int64Attribute{Computed: true}, "gpu_model": schema.StringAttribute{Computed: true},
		"status": schema.StringAttribute{Computed: true}, "public_ip": schema.StringAttribute{Computed: true}, "private_ip": schema.StringAttribute{Computed: true},
		// No default: the API requires the choice for a VM with an automatically assigned public IP and answers 400 without it.
		"delete_public_ip_action": schema.StringAttribute{Optional: true, Validators: []validator.String{computeOneOf("release", "reserve")}, Description: "What to do with the VM's automatically assigned public IP on destroy: release or reserve. The API requires it for a VM with an automatically assigned public IP and refuses the destroy with 400 when it is missing; when unset the provider sends no public_ip_action and relays that answer. Reserving an address may continue billing."},
		"ssh_key_ids":             schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(empty), Description: "Existing Secret Store SSH key IDs. Changes are applied in place through the VM access operation (keys are added before others are removed)."},
		"tags":                    schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(empty), PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}, Description: "VM tags. Changes replace the VM because the public API has no tag update operation."},
	}}
}

// A term the API never reported (legacy or imported VM) only records the
// configured value; a change to a known term replaces the VM.
func billingIntervalReplace() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull() && !req.PlanValue.IsUnknown() && !req.PlanValue.Equal(req.StateValue)
	}, "Changing a known billing term replaces the VM.", "Changing a known billing term replaces the VM.")
}
func (r *cloudVmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}
func (r *cloudVmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan cloudVmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.targetPlan(ctx, plan.SiteID.ValueString(), plan.PlanID.ValueString(), plan.BillingInterval.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve compute plan", err.Error())
		return
	}
	var keys, tags []string
	resp.Diagnostics.Append(plan.SSHKeyIDs.ElementsAs(ctx, &keys, false)...)
	resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tags, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "site_id": plan.SiteID.ValueString(), "os_distro": plan.OsDistro.ValueString(), "os_type": plan.OsType.ValueString(), "template_id": plan.TemplateID.ValueString(), "cpu": p.Cpu, "ram_mb": p.RamMb, "disk_gb": p.DiskGb, "plan_id": p.PlanID, "ssh_key_ids": keys, "tags": tags}
	if len(p.BillingCatalog) > 0 {
		body["billing_catalog"] = p.BillingCatalog
	}
	if r.kind() == "gpu" {
		body["gpu_count"] = p.GpuCount
		body["gpu_model"] = p.GpuModel
	}
	var accepted operationAccepted
	if err := r.client.doH(ctx, http.MethodPost, r.route(""), map[string]string{"X-Idempotency-Key": idempotencyKey()}, body, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to create VM", err.Error())
		return
	}
	if accepted.VmID == "" {
		resp.Diagnostics.AddError("Invalid VM create response", "API returned no vm_id. Check the portal before retrying to avoid duplicate infrastructure.")
		return
	}
	plan.ID = types.StringValue(accepted.VmID)
	plan.Cpu = types.Int64Value(p.Cpu)
	plan.RamMb = types.Int64Value(p.RamMb)
	plan.DiskGb = types.Int64Value(p.DiskGb)
	plan.GpuCount = types.Int64Value(p.GpuCount)
	plan.GpuModel = types.StringValue(p.GpuModel)
	plan.Status = types.StringValue(accepted.Status)
	plan.PublicIP = types.StringNull()
	plan.PrivateIP = types.StringNull()
	if plan.BillingInterval.IsUnknown() {
		plan.BillingInterval = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid VM create response", "API returned no operation_id; VM identity is retained for recovery.")
		return
	}
	if err := r.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("VM create did not complete", err.Error())
		return
	}
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to refresh created VM", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Temporary: the plan's billing_catalog is looked up and sent until the API resolves the SKU from plan_id itself.
func (r *cloudVmResource) targetPlan(ctx context.Context, siteID, planID, interval string) (*computePlan, error) {
	p, err := r.client.findPlanForTerm(ctx, r.kind(), siteID, planID, "", interval)
	if err != nil {
		return nil, err
	}
	if err := p.selectBillingTerm(interval); err != nil {
		return nil, err
	}
	return p, nil
}

// submit sends one VM action and waits for its operation.
func (r *cloudVmResource) submit(ctx context.Context, method, id, action string, body map[string]any) error {
	var accepted operationAccepted
	if err := r.client.doH(ctx, method, r.route(id)+"/actions/"+action, map[string]string{"X-Idempotency-Key": idempotencyKey()}, body, &accepted); err != nil {
		return err
	}
	if accepted.OperationID == "" {
		return fmt.Errorf("API accepted the %s request but returned no operation_id", action)
	}
	return r.client.waitOperation(ctx, accepted.OperationID, 0)
}

// resize moves the VM to another plan in place. The API runs its own precheck,
// stops and starts the VM when it must, and answers with an error otherwise.
func (r *cloudVmResource) resize(ctx context.Context, state cloudVmModel, planID string) error {
	p, err := r.targetPlan(ctx, state.SiteID.ValueString(), planID, state.BillingInterval.ValueString())
	if err != nil {
		return err
	}
	body := map[string]any{"requested_by": "terraform"}
	for key, value := range map[string]int64{"cpu": p.Cpu, "ram_mb": p.RamMb, "disk_gb": p.DiskGb} {
		if value > 0 {
			body[key] = value
		}
	}
	if len(p.BillingCatalog) > 0 {
		body["billing_catalog"] = p.BillingCatalog
	}
	return r.submit(ctx, http.MethodPost, state.ID.ValueString(), "resize", body)
}

// updateSSHKeys adds new keys before removing old ones through the access operation.
func (r *cloudVmResource) updateSSHKeys(ctx context.Context, id string, from, to types.Set) error {
	var before, after []string
	if d := from.ElementsAs(ctx, &before, false); d.HasError() {
		return fmt.Errorf("invalid SSH key state")
	}
	if d := to.ElementsAs(ctx, &after, false); d.HasError() {
		return fmt.Errorf("invalid SSH key configuration")
	}
	for _, change := range []struct {
		mode string
		ids  []string
	}{{"add", setDifference(after, before)}, {"remove", setDifference(before, after)}} {
		if len(change.ids) == 0 {
			continue
		}
		if err := r.submit(ctx, http.MethodPatch, id, "access", map[string]any{"ssh_key_mode": change.mode, "ssh_key_ids": change.ids, "requested_by": "terraform"}); err != nil {
			return fmt.Errorf("%s SSH keys: %w", change.mode, err)
		}
	}
	return nil
}

func setDifference(values, minus []string) []string {
	skip := make(map[string]bool, len(minus))
	for _, v := range minus {
		skip[v] = true
	}
	var out []string
	for _, v := range values {
		if !skip[v] {
			out = append(out, v)
		}
	}
	return out
}
func (r *cloudVmResource) refresh(ctx context.Context, state *cloudVmModel) error {
	var vm cloudVmAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(state.ID.ValueString()), nil, &vm); err != nil {
		return err
	}
	if vm.identifier() == "" || vm.Name == "" || vm.Status == "" {
		return fmt.Errorf("incomplete VM response; refusing to overwrite state")
	}
	if vm.identifier() != state.ID.ValueString() {
		return fmt.Errorf("VM response ID does not match requested VM")
	}
	// The deployed backend's VirtualMachine model returns these fields, unlike older public OpenAPI revisions.
	if vm.SiteID == nil || vm.PlanID == nil || vm.TemplateID == nil || *vm.SiteID == "" || *vm.PlanID == "" || *vm.TemplateID == "" {
		return fmt.Errorf("VM read must expose site_id, plan_id and template_id for reliable refresh/import; upgrade the public API contract")
	}
	// The term is whatever the API reports; a read without one keeps the recorded value.
	if vm.BillingCatalog != nil && vm.BillingCatalog.BillingInterval != "" {
		state.BillingInterval = types.StringValue(vm.BillingCatalog.BillingInterval)
	} else if state.BillingInterval.IsUnknown() {
		state.BillingInterval = types.StringNull()
	}
	state.Name = types.StringValue(vm.Name)
	state.SiteID = types.StringPointerValue(vm.SiteID)
	state.PlanID = types.StringPointerValue(vm.PlanID)
	state.TemplateID = types.StringPointerValue(vm.TemplateID)
	state.OsType = types.StringValue(vm.OsType)
	state.OsDistro = types.StringValue(vm.OsDistro)
	state.Cpu = types.Int64Value(vm.Cpu)
	state.RamMb = types.Int64Value(vm.RamMb)
	state.DiskGb = types.Int64Value(vm.DiskGb)
	state.GpuCount = types.Int64Value(vm.GpuCount)
	state.GpuModel = types.StringPointerValue(vm.GpuModel)
	state.Status = types.StringValue(vm.Status)
	state.PublicIP = types.StringPointerValue(vm.PublicIP)
	state.PrivateIP = types.StringPointerValue(vm.PrivateIP)
	if vm.SSHKeyIDs == nil || vm.Tags == nil || vm.OsType == "" || vm.OsDistro == "" {
		return fmt.Errorf("VM read must expose ssh_key_ids, tags and OS fields for reliable drift detection")
	}
	keys, d := types.SetValueFrom(ctx, types.StringType, *vm.SSHKeyIDs)
	if d.HasError() {
		return fmt.Errorf("invalid SSH keys in VM response: %v", d)
	}
	state.SSHKeyIDs = keys
	tags, d := types.SetValueFrom(ctx, types.StringType, *vm.Tags)
	if d.HasError() {
		return fmt.Errorf("invalid tags in VM response: %v", d)
	}
	state.Tags = tags
	return nil
}
func (r *cloudVmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state cloudVmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &state); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read VM", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
func (r *cloudVmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state cloudVmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.BillingInterval.IsUnknown() {
		state.BillingInterval = plan.BillingInterval
	}
	state.PublicIPAction = plan.PublicIPAction
	// Each completed change is saved before the next one starts.
	save := func(summary string, err error) bool {
		if err != nil {
			resp.Diagnostics.AddError(summary, err.Error())
			return false
		}
		if err := r.refresh(ctx, &state); err != nil {
			resp.Diagnostics.AddError("Failed to refresh VM", err.Error())
			return false
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return !resp.Diagnostics.HasError()
	}
	if !plan.PlanID.Equal(state.PlanID) && !save("Failed to resize VM", r.resize(ctx, state, plan.PlanID.ValueString())) {
		return
	}
	if !plan.SSHKeyIDs.Equal(state.SSHKeyIDs) && !save("Failed to update VM SSH keys", r.updateSSHKeys(ctx, state.ID.ValueString(), state.SSHKeyIDs, plan.SSHKeyIDs)) {
		return
	}
	save("Failed to refresh VM", nil)
}
func (r *cloudVmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state cloudVmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only a configured public_ip_action is sent. Without one the API decides:
	// it answers 400 for a VM with an automatically assigned public IP.
	var body any
	if action := state.PublicIPAction.ValueString(); action != "" {
		body = map[string]any{"public_ip_action": action}
	}
	var accepted operationAccepted
	err := r.client.doH(ctx, http.MethodDelete, r.route(state.ID.ValueString()), map[string]string{"X-Idempotency-Key": idempotencyKey()}, body, &accepted)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete VM", err.Error())
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid VM delete response", "API returned no operation_id; VM remains tracked until deletion can be confirmed.")
		return
	}
	if err := r.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("VM delete did not complete", err.Error())
	}
}
func (r *cloudVmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
