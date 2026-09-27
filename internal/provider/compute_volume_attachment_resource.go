package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"net/url"
	"strings"
)

type vmVolumeAttachmentResource struct {
	client *Client
	vmType string
}

func NewCloudVmVolumeAttachmentResource() resource.Resource {
	return &vmVolumeAttachmentResource{vmType: "cloud"}
}
func NewGpuVmVolumeAttachmentResource() resource.Resource {
	return &vmVolumeAttachmentResource{vmType: "gpu"}
}

var _ resource.ResourceWithImportState = (*vmVolumeAttachmentResource)(nil)

type vmDataVolume struct {
	VolumeID          string  `json:"volume_id"`
	Mode              string  `json:"mode"`
	GuestDevice       *string `json:"guest_device"`
	MountInstructions *string `json:"mount_instructions"`
}
type vmVolumeAttachmentModel struct {
	ID                types.String `tfsdk:"id"`
	VmID              types.String `tfsdk:"vm_id"`
	VolumeID          types.String `tfsdk:"volume_id"`
	Mode              types.String `tfsdk:"mode"`
	ConfirmUnmounted  types.Bool   `tfsdk:"confirm_unmounted"`
	GuestDevice       types.String `tfsdk:"guest_device"`
	MountInstructions types.String `tfsdk:"mount_instructions"`
}

func (r *vmVolumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.vmType + "_vm_volume_attachment"
}
func (r *vmVolumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "Attaches an existing block volume to a VM. Destroy detaches the volume without deleting it. The guest must be unmounted before destruction. Import using VM_ID/VOLUME_ID. Read requires the backend VM data_volumes projection.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}, "vm_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}}, "volume_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{computeNonEmpty()}}, "mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("single-writer"), PlanModifiers: replace, Validators: []validator.String{computeOneOf("single-writer", "multi-writer")}}, "confirm_unmounted": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Explicit acknowledgement passed to detach. Set true only after unmounting the volume inside the guest. Forced detach is never used."}, "guest_device": schema.StringAttribute{Computed: true}, "mount_instructions": schema.StringAttribute{Computed: true},
	}}
}
func (r *vmVolumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *vmVolumeAttachmentResource) route(vmID string) string {
	return "/compute/" + r.vmType + "-vms/" + url.PathEscape(vmID)
}
func (r *vmVolumeAttachmentResource) refresh(ctx context.Context, m *vmVolumeAttachmentModel) (bool, error) {
	var vm cloudVmAPI
	if err := r.client.do(ctx, http.MethodGet, r.route(m.VmID.ValueString()), nil, &vm); err != nil {
		return false, err
	}
	if vm.identifier() != m.VmID.ValueString() {
		return false, fmt.Errorf("VM response identity does not match attachment")
	}
	if vm.DataVolumes == nil {
		return false, fmt.Errorf("VM API must expose data_volumes to manage attachments safely; upgrade the public API contract")
	}
	for _, v := range *vm.DataVolumes {
		if v.VolumeID == m.VolumeID.ValueString() {
			if v.Mode == "" {
				return false, fmt.Errorf("volume attachment response has no mode")
			}
			m.ID = types.StringValue(m.VmID.ValueString() + "/" + m.VolumeID.ValueString())
			m.Mode = types.StringValue(v.Mode)
			m.GuestDevice = types.StringPointerValue(v.GuestDevice)
			m.MountInstructions = types.StringPointerValue(v.MountInstructions)
			if m.ConfirmUnmounted.IsNull() || m.ConfirmUnmounted.IsUnknown() {
				m.ConfirmUnmounted = types.BoolValue(false)
			}
			return true, nil
		}
	}
	return false, nil
}
func (r *vmVolumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m vmVolumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	probe := m
	found, err := r.refresh(ctx, &probe)
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect volume attachments", err.Error())
		return
	}
	if found {
		resp.Diagnostics.AddError("Volume is already attached", "Import this attachment using "+m.VmID.ValueString()+"/"+m.VolumeID.ValueString()+" before managing it.")
		return
	}
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Volume attachment billing eligibility denied", err.Error())
		return
	}
	var accepted operationAccepted
	if err := r.client.doH(ctx, http.MethodPost, r.route(m.VmID.ValueString())+"/actions/attach-volume", map[string]string{"X-Idempotency-Key": idempotencyKey()}, map[string]any{"volume_id": m.VolumeID.ValueString(), "mode": m.Mode.ValueString(), "requested_by": "terraform"}, &accepted); err != nil {
		resp.Diagnostics.AddError("Failed to attach volume", err.Error())
		return
	}
	m.ID = types.StringValue(m.VmID.ValueString() + "/" + m.VolumeID.ValueString())
	m.GuestDevice = types.StringNull()
	m.MountInstructions = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid attach response", "API returned no operation_id; attachment identity is retained for recovery.")
		return
	}
	if err := r.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("Volume attach did not complete", err.Error())
		return
	}
	found, err = r.refresh(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh attachment", err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddError("Volume attach unconfirmed", "Operation succeeded but the VM does not report this attachment.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmVolumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m vmVolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.refresh(ctx, &m)
	if IsNotFound(err) || (err == nil && !found) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read volume attachment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmVolumeAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m vmVolumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.refresh(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Failed to refresh volume attachment", err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddError("Volume attachment disappeared", "Refresh and apply to restore the attachment.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *vmVolumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m vmVolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := r.refresh(ctx, &m)
	if IsNotFound(err) || (err == nil && !found) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to inspect volume attachment", err.Error())
		return
	}
	var accepted operationAccepted
	err = r.client.doH(ctx, http.MethodPost, r.route(m.VmID.ValueString())+"/actions/detach-volume", map[string]string{"X-Idempotency-Key": idempotencyKey()}, map[string]any{"volume_id": m.VolumeID.ValueString(), "force": false, "confirm_unmounted": m.ConfirmUnmounted.ValueBool(), "requested_by": "terraform"}, &accepted)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to detach volume", err.Error())
		return
	}
	if accepted.OperationID == "" {
		resp.Diagnostics.AddError("Invalid detach response", "API returned no operation_id; attachment remains tracked.")
		return
	}
	if err := r.client.waitOperation(ctx, accepted.OperationID, 0); err != nil {
		resp.Diagnostics.AddError("Volume detach did not complete", err.Error())
		return
	}
	found, err = r.refresh(ctx, &m)
	if IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to verify volume detach", err.Error())
		return
	}
	if found {
		resp.Diagnostics.AddError("Volume detach unconfirmed", "The VM still reports this volume attachment.")
	}
}
func (r *vmVolumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected VM_ID/VOLUME_ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), parts[1])...)
}
