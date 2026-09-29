package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	resp.Schema = schema.Schema{Description: "A " + r.kind() + " VM priced from the public compute catalog. Configuration changes replace the VM. Imports use the VM ID. Public-IP release is explicit; no payment is initiated by Terraform.", Attributes: map[string]schema.Attribute{
		"id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name": required("VM name."), "site_id": required("Placement site from ibee_sites."), "plan_id": required("Selectable, priced plan from ibee_compute_plans."), "template_id": required("Image template from ibee_images."), "os_distro": required("Image OS distribution."),
		"billing_interval": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{computeBillingIntervalDefault{}, stringplanmodifier.RequiresReplace()}, Validators: []validator.String{computeOneOf("HOURLY", "MONTHLY")}, Description: "Catalog billing term. Omission preserves an existing or imported VM's term and selects uncommitted HOURLY for a new VM. MONTHLY selects the advertised one-month commitment; deleting a VM does not cancel contractual charges. Explicit term changes replace the VM."},
		"os_type":          schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("linux"), PlanModifiers: replace, Validators: []validator.String{computeOneOf(osTypes...)}},
		"cpu":              schema.Int64Attribute{Computed: true}, "ram_mb": schema.Int64Attribute{Computed: true}, "disk_gb": schema.Int64Attribute{Computed: true}, "gpu_count": schema.Int64Attribute{Computed: true}, "gpu_model": schema.StringAttribute{Computed: true},
		"status": schema.StringAttribute{Computed: true}, "public_ip": schema.StringAttribute{Computed: true}, "private_ip": schema.StringAttribute{Computed: true},
		"delete_public_ip_action": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("release"), Validators: []validator.String{computeOneOf("release", "reserve")}, Description: "release (default) or reserve the automatic public IP on destroy. Reserving an address may continue billing."},
		"ssh_key_ids":             schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(empty), PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}, Description: "Existing Secret Store SSH key IDs. Changes replace the VM."},
		"tags":                    schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(empty), PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}, Description: "VM tags. Changes replace the VM because the public API has no tag update operation."},
	}}
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
	account, err := r.client.checkBillingEligibility(ctx, billingEligibilityRequest{})
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve billing currency", err.Error())
		return
	}
	if err := billingAdmissionError(account); err != nil {
		resp.Diagnostics.AddError("VM billing eligibility denied", err.Error())
		return
	}
	p, err := r.client.findPlanForTerm(ctx, r.kind(), plan.SiteID.ValueString(), plan.PlanID.ValueString(), account.Currency, plan.BillingInterval.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to resolve compute plan", err.Error())
		return
	}
	if err := p.selectBillingTerm(plan.BillingInterval.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unsupported VM billing term", err.Error())
		return
	}
	if err := p.validate(r.kind()); err != nil {
		resp.Diagnostics.AddError("Invalid compute plan", err.Error())
		return
	}
	var keys, tags []string
	resp.Diagnostics.Append(plan.SSHKeyIDs.ElementsAs(ctx, &keys, false)...)
	resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tags, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.requireBillingEligibilityForCurrency(ctx, p.Code, p.estimatedCost(), p.Currency); err != nil {
		resp.Diagnostics.AddError("VM billing eligibility denied", err.Error())
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "site_id": plan.SiteID.ValueString(), "os_distro": plan.OsDistro.ValueString(), "os_type": plan.OsType.ValueString(), "template_id": plan.TemplateID.ValueString(), "cpu": p.Cpu, "ram_mb": p.RamMb, "disk_gb": p.DiskGb, "plan_id": p.PlanID, "ssh_key_ids": keys, "tags": tags}
	// This catalog extension is exposed by public_compute_catalog.py; older OpenAPI revisions omit it.
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
func (p *computePlan) validate(vmType string) error {
	if len(p.BillingCatalog) == 0 {
		return fmt.Errorf("plan %q has no canonical billing_catalog; public catalog alignment is required", p.PlanID)
	}
	if sku, ok := p.BillingCatalog["sku_code"].(string); !ok || sku != p.Code {
		return fmt.Errorf("plan %q has inconsistent SKU identity", p.PlanID)
	}
	if !p.Selectable || p.PricingStatus != "priced" {
		return fmt.Errorf("plan %q is not selectable and priced", p.PlanID)
	}
	if p.Cpu < 1 || p.RamMb < 512 || p.DiskGb < 10 || p.Code == "" {
		return fmt.Errorf("plan %q has incomplete sizing or SKU identity", p.PlanID)
	}
	if vmType == "gpu" && (p.GpuCount < 1 || p.GpuModel == "") {
		return fmt.Errorf("plan %q has no GPU model/count", p.PlanID)
	}
	if p.estimatedCost() == nil || *p.estimatedCost() < 0 {
		return fmt.Errorf("plan %q has no trusted price for billing interval %q", p.PlanID, p.BillingInterval)
	}
	return nil
}
func (p *computePlan) estimatedCost() *int64 {
	if p.SelectedTermCostMinor != nil {
		return p.SelectedTermCostMinor
	}
	switch strings.ToLower(p.BillingInterval) {
	case "hourly":
		return p.HourlyPriceMinor
	case "monthly":
		return p.MonthlyPriceMinor
	default:
		return nil
	}
}
func (r *cloudVmResource) refresh(ctx context.Context, state *cloudVmModel) error {
	var vm cloudVmAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(state.ID.ValueString()), nil, &vm); err != nil {
		return err
	}
	if vm.identifier() == "" || vm.Name == "" || vm.Status == "" || vm.Cpu < 1 || vm.RamMb < 512 || vm.DiskGb < 10 {
		return fmt.Errorf("incomplete VM response; refusing to overwrite state")
	}
	if vm.identifier() != state.ID.ValueString() {
		return fmt.Errorf("VM response ID does not match requested VM")
	}
	// The deployed backend's VirtualMachine model returns these fields, unlike older public OpenAPI revisions.
	if vm.SiteID == nil || vm.PlanID == nil || vm.TemplateID == nil || *vm.SiteID == "" || *vm.PlanID == "" || *vm.TemplateID == "" {
		return fmt.Errorf("VM read must expose site_id, plan_id and template_id for reliable refresh/import; upgrade the public API contract")
	}
	if vm.BillingCatalog == nil || (vm.BillingCatalog.BillingInterval != "HOURLY" && vm.BillingCatalog.BillingInterval != "MONTHLY") {
		return fmt.Errorf("VM read must expose selected billing_catalog.billing_interval for safe billing-term refresh/import; an unselected legacy catalog cannot establish contractual intent")
	}
	if err := vm.BillingCatalog.validateCanonical(); err != nil {
		return fmt.Errorf("unsupported canonical VM billing commitment: %w", err)
	}
	state.BillingInterval = types.StringValue(vm.BillingCatalog.BillingInterval)
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
	if state.PublicIPAction.IsNull() || state.PublicIPAction.IsUnknown() {
		state.PublicIPAction = types.StringValue("release")
	}
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
	var plan cloudVmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to refresh VM", err.Error())
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
	var accepted operationAccepted
	err := r.client.doH(ctx, http.MethodDelete, r.route(state.ID.ValueString()), map[string]string{"X-Idempotency-Key": idempotencyKey()}, map[string]any{"public_ip_action": action}, &accepted)
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
